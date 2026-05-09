//go:build windows

package ui

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modUser32 = windows.NewLazyDLL("user32.dll")
	modGDI32  = windows.NewLazyDLL("gdi32.dll")

	procRegisterClassEx    = modUser32.NewProc("RegisterClassExW")
	procCreateWindowEx     = modUser32.NewProc("CreateWindowExW")
	procShowWindow         = modUser32.NewProc("ShowWindow")
	procUpdateWindow       = modUser32.NewProc("UpdateWindow")
	procGetMessage         = modUser32.NewProc("GetMessageW")
	procTranslateMessage   = modUser32.NewProc("TranslateMessage")
	procDispatchMessage    = modUser32.NewProc("DispatchMessageW")
	procDefWindowProc      = modUser32.NewProc("DefWindowProcW")
	procDestroyWindow      = modUser32.NewProc("DestroyWindow")
	procPostQuitMessage    = modUser32.NewProc("PostQuitMessage")
	procLoadCursor         = modUser32.NewProc("LoadCursorW")
	procGetDC              = modUser32.NewProc("GetDC")
	procReleaseDC          = modUser32.NewProc("ReleaseDC")
	procBeginPaint         = modUser32.NewProc("BeginPaint")
	procEndPaint           = modUser32.NewProc("EndPaint")
	procDrawText           = modUser32.NewProc("DrawTextW")
	procCreateFont         = modGDI32.NewProc("CreateFontW")
	procSelectObject       = modGDI32.NewProc("SelectObject")
	procDeleteObject       = modGDI32.NewProc("DeleteObject")
	procSetBkMode          = modGDI32.NewProc("SetBkMode")
	procSetTextColor       = modGDI32.NewProc("SetTextColor")
	procFillRect           = modUser32.NewProc("FillRect")
	procCreateSolidBrush   = modGDI32.NewProc("CreateSolidBrush")
	procGetModuleHandle    = windows.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW")
	procSetWindowText      = modUser32.NewProc("SetWindowTextW")
	procMessageBox         = modUser32.NewProc("MessageBoxW")
	procGetClientRect      = modUser32.NewProc("GetClientRect")
	procInvalidateRect     = modUser32.NewProc("InvalidateRect")
	procSetForegroundWindow = modUser32.NewProc("SetForegroundWindow")
)

const (
	wsOverlappedWindow = 0x00CF0000
	wsVisible          = 0x10000000
	swShow             = 5
	wmDestroy          = 0x0002
	wmPaint            = 0x000F
	wmCommand          = 0x0111
	wmClose            = 0x0010
	idcArrow           = 32512
	transparent        = 1
	dtCenter           = 0x00000001
	dtVCenter          = 0x00000004
	dtSingleLine       = 0x00000020
	dtNoClip           = 0x00000100
	btnID              = 101
	wsChild            = 0x40000000
	wsTabStop          = 0x00010000
	bsPushButton       = 0x00000000
	wsGroupBox         = wsChild | wsVisible
	mbOk               = 0x00000000
)

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type point struct{ X, Y int32 }

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

type rect struct{ Left, Top, Right, Bottom int32 }

type paintStruct struct {
	Hdc         uintptr
	FErase      int32
	RcPaint     rect
	FRestore    int32
	FIncUpdate  int32
	RgbReserved [32]byte
}

var (
	globalCode     string
	globalHwnd     uintptr
	globalOnQuit   func()
	mu             sync.Mutex
	bgColor        = colorRef(0x17, 0x1a, 0x27)   // dark navy
	codeColor      = colorRef(0xf1, 0xf5, 0xf9)   // near-white
	labelColor     = colorRef(0x64, 0x74, 0x8b)   // slate
	accentColor    = colorRef(0x63, 0x66, 0xf1)   // indigo
)

func colorRef(r, g, b byte) uintptr {
	return uintptr(r) | uintptr(g)<<8 | uintptr(b)<<16
}

func wndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch uint32(msg) {
	case wmPaint:
		return onPaint(hwnd)
	case wmCommand:
		if wParam&0xFFFF == btnID {
			if globalOnQuit != nil {
				globalOnQuit()
			}
			procDestroyWindow.Call(hwnd)
		}
	case wmClose:
		if globalOnQuit != nil {
			globalOnQuit()
		}
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProc.Call(hwnd, msg, wParam, lParam)
	return r
}

func onPaint(hwnd uintptr) uintptr {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	defer procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))

	var rc rect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))

	// Background
	bg, _, _ := procCreateSolidBrush.Call(bgColor)
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), bg)
	procDeleteObject.Call(bg)

	// Accent bar at top
	topBar := rect{rc.Left, rc.Top, rc.Right, rc.Top + 4}
	accent, _, _ := procCreateSolidBrush.Call(accentColor)
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&topBar)), accent)
	procDeleteObject.Call(accent)

	procSetBkMode.Call(hdc, transparent)

	// Label: "RemoteMaster"
	labelFont, _, _ := procCreateFont.Call(
		13, 0, 0, 0, 400, 0, 0, 0, 0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(mustUTF16("Segoe UI"))),
	)
	procSelectObject.Call(hdc, labelFont)
	procSetTextColor.Call(hdc, labelColor)
	appLabel := rect{rc.Left + 16, rc.Top + 16, rc.Right - 16, rc.Top + 36}
	drawTextW(hdc, "REMOTEMASTER", &appLabel, dtNoClip)
	procDeleteObject.Call(labelFont)

	// Label: "Session Code"
	smallFont, _, _ := procCreateFont.Call(
		12, 0, 0, 0, 600, 0, 0, 0, 0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(mustUTF16("Segoe UI"))),
	)
	procSelectObject.Call(hdc, smallFont)
	procSetTextColor.Call(hdc, labelColor)
	subRect := rect{rc.Left + 16, rc.Top + 44, rc.Right - 16, rc.Top + 62}
	drawTextW(hdc, "Your session code is:", &subRect, dtNoClip)
	procDeleteObject.Call(smallFont)

	// Big code display
	codeFont, _, _ := procCreateFont.Call(
		54, 0, 0, 0, 700, 0, 0, 0, 0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(mustUTF16("Segoe UI"))),
	)
	procSelectObject.Call(hdc, codeFont)
	procSetTextColor.Call(hdc, codeColor)

	mu.Lock()
	code := globalCode
	mu.Unlock()

	var display string
	switch code {
	case "":
		display = "· · · · · ·" // connecting, waiting for server
	case "------":
		display = "------" // disconnected / session ended
	case "NOCONN":
		display = "NO CONN" // dial error — wrong URL or server down
	default:
		display = code[:3] + " " + code[3:]
	}
	codeRect := rect{rc.Left, rc.Top + 58, rc.Right, rc.Top + 125}
	drawTextW(hdc, display, &codeRect, dtCenter|dtNoClip)
	procDeleteObject.Call(codeFont)

	// Hint
	hintFont, _, _ := procCreateFont.Call(
		11, 0, 0, 0, 400, 0, 0, 0, 0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(mustUTF16("Segoe UI"))),
	)
	procSelectObject.Call(hdc, hintFont)
	procSetTextColor.Call(hdc, labelColor)
	hintRect := rect{rc.Left + 16, rc.Top + 126, rc.Right - 16, rc.Top + 145}
	var hintText string
	switch code {
	case "NOCONN":
		hintText = "Cannot reach server — check URL scheme (ws:// vs wss://)"
	case "":
		hintText = "Connecting to server..."
	default:
		hintText = "Provide this code to your support agent"
	}
	drawTextW(hdc, hintText, &hintRect, dtNoClip)
	procDeleteObject.Call(hintFont)

	return 0
}

func drawTextW(hdc uintptr, s string, rc *rect, flags uint32) {
	ptr, _ := syscall.UTF16PtrFromString(s)
	procDrawText.Call(hdc, uintptr(unsafe.Pointer(ptr)), ^uintptr(0), uintptr(unsafe.Pointer(rc)), uintptr(flags))
}

func mustUTF16(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

// Run creates and runs the window. code is displayed initially; SetCode can update it.
// onQuit is called when the user clicks "End Session" or closes the window.
// Run blocks until the window is closed.
func Run(code string, onQuit func()) error {
	mu.Lock()
	globalCode = code
	globalOnQuit = onQuit
	mu.Unlock()

	hInst, _, _ := procGetModuleHandle.Call(0)
	cursor, _, _ := procLoadCursor.Call(0, idcArrow)

	className := mustUTF16("remotemaster_wnd")
	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   syscall.NewCallback(wndProc),
		HInstance:     hInst,
		HCursor:       cursor,
		HbrBackground: 0,
		LpszClassName: className,
	}
	if r, _, _ := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("RegisterClassEx failed")
	}

	title := mustUTF16("RemoteMaster")
	hwnd, _, _ := procCreateWindowEx.Call(
		0, // dwExStyle
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow&^0x00040000&^0x00020000, // remove maximize/resize
		0x80000000, 0x80000000,                      // CW_USEDEFAULT
		320, 200,
		0, 0, hInst, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowEx failed")
	}
	globalHwnd = hwnd

	// End Session button
	btnLabel := mustUTF16("End Session")
	btnClass := mustUTF16("BUTTON")
	procCreateWindowEx.Call(
		0,
		uintptr(unsafe.Pointer(btnClass)),
		uintptr(unsafe.Pointer(btnLabel)),
		wsChild|wsVisible|wsTabStop|bsPushButton,
		16, 156, 288, 30,
		hwnd, btnID, hInst, 0,
	)

	procShowWindow.Call(hwnd, swShow)
	procSetForegroundWindow.Call(hwnd)
	procUpdateWindow.Call(hwnd)

	var m msg
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if r == 0 || int32(r) == -1 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
	return nil
}

// SetCode updates the displayed session code and redraws.
func SetCode(code string) {
	mu.Lock()
	globalCode = code
	mu.Unlock()
	if globalHwnd != 0 {
		procInvalidateRect.Call(globalHwnd, 0, 1)
	}
}
