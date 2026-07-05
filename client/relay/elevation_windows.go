//go:build windows

package relay

import (
	"context"
	"log"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

var (
	elevUser32                   = windows.NewLazySystemDLL("user32.dll")
	procGetForegroundWindow      = elevUser32.NewProc("GetForegroundWindow")
	procGetWindowThreadProcessID = elevUser32.NewProc("GetWindowThreadProcessId")
)

// elevatedInputNotice is shown on both ends when remote input is being
// silently discarded by Windows.
const elevatedInputNotice = "Focused app is elevated (e.g. Task Manager) — Windows blocks remote input. Restart RemoteMaster as administrator to control it."

// watchElevatedFocus warns both sides when the remote user focuses an
// elevated window. SendInput into a higher-integrity process is silently
// discarded by Windows (UIPI) — no error is ever reported at the injection
// site — so without this the agent just sees clicks doing nothing.
func (c *Client) watchElevatedFocus(ctx context.Context, conn *websocket.Conn) {
	if windows.GetCurrentProcessToken().IsElevated() {
		return // running as admin: input reaches elevated windows too
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	warned := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		elevated := foregroundIsElevated()
		if elevated == warned {
			continue
		}
		warned = elevated

		notice := ""
		if elevated {
			notice = elevatedInputNotice
			log.Printf("input: %s", notice)
		} else {
			log.Printf("input: elevated app no longer focused; input restored")
		}
		if c.OnNotice != nil {
			c.OnNotice(notice)
		}
		// Relay the notice to the viewer so the support agent knows their
		// clicks are being dropped (empty msg clears it).
		_ = wsjson.Write(ctx, conn, ctrlMsg{Type: "notice", Msg: notice})
	}
}

// foregroundIsElevated reports whether the focused window belongs to a
// process running at a higher integrity level than us. Failure to open the
// process or its token is itself the UIPI signature, so it counts as
// elevated.
func foregroundIsElevated() bool {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return false
	}
	var pid uint32
	procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 || pid == uint32(windows.GetCurrentProcessId()) {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return true
	}
	defer windows.CloseHandle(h)
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return true
	}
	defer tok.Close()
	return tok.IsElevated()
}
