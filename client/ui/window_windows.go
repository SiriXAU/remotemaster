//go:build windows

package ui

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modUser32 = windows.NewLazyDLL("user32.dll")
	modGDI32  = windows.NewLazyDLL("gdi32.dll")

	procRegisterClassEx     = modUser32.NewProc("RegisterClassExW")
	procCreateWindowEx      = modUser32.NewProc("CreateWindowExW")
	procShowWindow          = modUser32.NewProc("ShowWindow")
	procUpdateWindow        = modUser32.NewProc("UpdateWindow")
	procGetMessage          = modUser32.NewProc("GetMessageW")
	procTranslateMessage    = modUser32.NewProc("TranslateMessage")
	procDispatchMessage     = modUser32.NewProc("DispatchMessageW")
	procDefWindowProc       = modUser32.NewProc("DefWindowProcW")
	procDestroyWindow       = modUser32.NewProc("DestroyWindow")
	procPostQuitMessage     = modUser32.NewProc("PostQuitMessage")
	procLoadCursor          = modUser32.NewProc("LoadCursorW")
	procBeginPaint          = modUser32.NewProc("BeginPaint")
	procEndPaint            = modUser32.NewProc("EndPaint")
	procDrawText            = modUser32.NewProc("DrawTextW")
	procCreateFont          = modGDI32.NewProc("CreateFontW")
	procSelectObject        = modGDI32.NewProc("SelectObject")
	procDeleteObject        = modGDI32.NewProc("DeleteObject")
	procSetBkMode           = modGDI32.NewProc("SetBkMode")
	procSetTextColor        = modGDI32.NewProc("SetTextColor")
	procFillRect            = modUser32.NewProc("FillRect")
	procCreateSolidBrush    = modGDI32.NewProc("CreateSolidBrush")
	procGetModuleHandle     = windows.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW")
	procGetClientRect       = modUser32.NewProc("GetClientRect")
	procInvalidateRect      = modUser32.NewProc("InvalidateRect")
	procSetForegroundWindow = modUser32.NewProc("SetForegroundWindow")
	procSendMessage         = modUser32.NewProc("SendMessageW")
	procMoveWindow          = modUser32.NewProc("MoveWindow")
	procSetWindowPos        = modUser32.NewProc("SetWindowPos")
	procGetDpiForWindow     = modUser32.NewProc("GetDpiForWindow")
	procAdjustWindowRectEx  = modUser32.NewProc("AdjustWindowRectEx")
	procSystemParametersInf = modUser32.NewProc("SystemParametersInfoW")

	procSetProcessDpiAwarenessContext = modUser32.NewProc("SetProcessDpiAwarenessContext")
)

const (
	wsVisible    = 0x10000000
	wsCaption    = 0x00C00000
	wsSysMenu    = 0x00080000
	wsMinimize   = 0x00020000
	swShow       = 5
	wmDestroy    = 0x0002
	wmPaint      = 0x000F
	wmCommand    = 0x0111
	wmClose      = 0x0010
	wmSetFont    = 0x0030
	wmEraseBkgnd = 0x0014
	wmDpiChanged = 0x02E0
	idcArrow     = 32512
	transparent  = 1
	dtCenter     = 0x00000001
	dtSingleLine = 0x00000020
	dtNoClip     = 0x00000100
	btnID        = 101
	wsChild      = 0x40000000
	wsTabStop    = 0x00010000
	bsFlat       = 0x00008000
	spiGetWork   = 0x0030 // SPI_GETWORKAREA
	swpNoZOrder  = 0x0004
	swpNoActive  = 0x0010

	// windowStyle: fixed-size dialog look — caption, close, minimize.
	windowStyle = wsCaption | wsSysMenu | wsMinimize

	// dpiAwarenessPerMonitorV2 is DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2.
	dpiAwarenessPerMonitorV2 = ^uintptr(3) // (DPI_AWARENESS_CONTEXT)-4

	// Base client-area size in 96-DPI units; scaled by the monitor DPI.
	baseW = 380
	baseH = 244
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
	globalCode   string
	globalStatus string
	globalHwnd   uintptr
	globalBtn    uintptr
	globalBtnFnt uintptr
	globalOnQuit func()
	mu           sync.Mutex

	bgColor     = colorRef(0x14, 0x17, 0x24) // dark navy
	codeColor   = colorRef(0xf8, 0xfa, 0xfc) // near-white
	labelColor  = colorRef(0x94, 0xa3, 0xb8) // light slate
	dimColor    = colorRef(0x64, 0x74, 0x8b) // slate
	accentColor = colorRef(0x63, 0x66, 0xf1) // indigo
	okColor     = colorRef(0x4a, 0xde, 0x80) // green
	errColor    = colorRef(0xf8, 0x71, 0x71) // red
)

func colorRef(r, g, b byte) uintptr {
	return uintptr(r) | uintptr(g)<<8 | uintptr(b)<<16
}

// dpiFor returns the window's DPI, defaulting to 96 on pre-1607 Windows.
func dpiFor(hwnd uintptr) int32 {
	if procGetDpiForWindow.Find() == nil {
		if dpi, _, _ := procGetDpiForWindow.Call(hwnd); dpi != 0 {
			return int32(dpi)
		}
	}
	return 96
}

// scale converts a 96-DPI design unit to device pixels.
func scale(v, dpi int32) int32 { return v * dpi / 96 }

func wndProc(hwnd, m, wParam, lParam uintptr) uintptr {
	switch uint32(m) {
	case wmPaint:
		return onPaint(hwnd)
	case wmEraseBkgnd:
		// Everything is painted in WM_PAINT; skipping the erase avoids a
		// white flash on each code/status update.
		return 1
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
	case wmDpiChanged:
		// Move to the system-suggested rect for the new monitor DPI and
		// re-lay-out the button.
		if lParam != 0 {
			rc := (*rect)(unsafe.Pointer(lParam))
			procSetWindowPos.Call(hwnd, 0,
				uintptr(rc.Left), uintptr(rc.Top),
				uintptr(rc.Right-rc.Left), uintptr(rc.Bottom-rc.Top),
				swpNoZOrder|swpNoActive)
		}
		layoutButton(hwnd)
		procInvalidateRect.Call(hwnd, 0, 0)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProc.Call(hwnd, m, wParam, lParam)
	return r
}

// makeFont creates a Segoe UI font at the given 96-DPI pixel size and weight.
func makeFont(px, weight, dpi int32) uintptr {
	f, _, _ := procCreateFont.Call(
		uintptr(^uintptr(0)-uintptr(scale(px, dpi))+1), // negative = glyph height
		0, 0, 0, uintptr(weight),
		0, 0, 0, 0, 0, 0,
		4, // ANTIALIASED_QUALITY
		0,
		uintptr(unsafe.Pointer(mustUTF16("Segoe UI"))),
	)
	return f
}

func onPaint(hwnd uintptr) uintptr {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	defer procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))

	dpi := dpiFor(hwnd)
	var rc rect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))

	// Background
	bg, _, _ := procCreateSolidBrush.Call(bgColor)
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), bg)
	procDeleteObject.Call(bg)

	// Accent bar at top
	topBar := rect{rc.Left, rc.Top, rc.Right, rc.Top + scale(4, dpi)}
	accent, _, _ := procCreateSolidBrush.Call(accentColor)
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&topBar)), accent)
	procDeleteObject.Call(accent)

	procSetBkMode.Call(hdc, transparent)

	mu.Lock()
	code := globalCode
	status := globalStatus
	mu.Unlock()

	// State-dependent content.
	display := code
	displayColor := codeColor
	hint := "Read this code to your support agent"
	hintColor := labelColor
	switch code {
	case "":
		display = "· · ·  · · ·"
		displayColor = dimColor
		hint = "Connecting to server..."
		hintColor = dimColor
	case "------":
		display = "— — —"
		displayColor = dimColor
		hint = "Session ended — waiting for a new code"
		hintColor = dimColor
	case "NOCONN":
		display = "NO LINK"
		displayColor = errColor
		hint = "Cannot reach the server — check your connection"
		hintColor = errColor
	default:
		if len(code) == 6 {
			display = code[:3] + "  " + code[3:]
		}
	}
	if status != "" {
		hint = status
		hintColor = okColor
	}

	margin := scale(24, dpi)

	// Brand
	brandFont := makeFont(13, 600, dpi)
	procSelectObject.Call(hdc, brandFont)
	procSetTextColor.Call(hdc, dimColor)
	brandRect := rect{rc.Left + margin, rc.Top + scale(20, dpi), rc.Right - margin, rc.Top + scale(40, dpi)}
	drawTextW(hdc, "R E M O T E M A S T E R", &brandRect, dtCenter|dtNoClip)
	procDeleteObject.Call(brandFont)

	// "Your session code is:"
	subFont := makeFont(15, 400, dpi)
	procSelectObject.Call(hdc, subFont)
	procSetTextColor.Call(hdc, labelColor)
	subRect := rect{rc.Left + margin, rc.Top + scale(52, dpi), rc.Right - margin, rc.Top + scale(74, dpi)}
	drawTextW(hdc, "Your session code is", &subRect, dtCenter|dtNoClip)
	procDeleteObject.Call(subFont)

	// Big code
	codeFont := makeFont(56, 700, dpi)
	procSelectObject.Call(hdc, codeFont)
	procSetTextColor.Call(hdc, displayColor)
	codeRect := rect{rc.Left, rc.Top + scale(78, dpi), rc.Right, rc.Top + scale(146, dpi)}
	drawTextW(hdc, display, &codeRect, dtCenter|dtNoClip)
	procDeleteObject.Call(codeFont)

	// Hint / status line
	hintFont := makeFont(13, 400, dpi)
	procSelectObject.Call(hdc, hintFont)
	procSetTextColor.Call(hdc, hintColor)
	hintRect := rect{rc.Left + margin, rc.Top + scale(150, dpi), rc.Right - margin, rc.Top + scale(172, dpi)}
	drawTextW(hdc, hint, &hintRect, dtCenter|dtNoClip)
	procDeleteObject.Call(hintFont)

	return 0
}

// layoutButton positions the End Session button for the current DPI and
// refreshes its font.
func layoutButton(hwnd uintptr) {
	if globalBtn == 0 {
		return
	}
	dpi := dpiFor(hwnd)
	var rc rect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	margin := scale(24, dpi)
	btnH := scale(40, dpi)
	procMoveWindow.Call(globalBtn,
		uintptr(rc.Left+margin),
		uintptr(rc.Bottom-btnH-scale(20, dpi)),
		uintptr(rc.Right-rc.Left-2*margin),
		uintptr(btnH), 1)

	old := globalBtnFnt
	globalBtnFnt = makeFont(15, 600, dpi)
	procSendMessage.Call(globalBtn, wmSetFont, globalBtnFnt, 1)
	if old != 0 {
		procDeleteObject.Call(old)
	}
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
	// A Win32 window only receives messages on the OS thread that created
	// it. Without pinning, the Go scheduler can migrate this goroutine to
	// another thread, orphaning the message pump — Windows then marks the
	// window "Not Responding".
	runtime.LockOSThread()

	// Crisp text on scaled displays (ignored on older Windows).
	if procSetProcessDpiAwarenessContext.Find() == nil {
		procSetProcessDpiAwarenessContext.Call(dpiAwarenessPerMonitorV2)
	}

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

	// Size the window for the primary monitor's DPI and center it in the
	// work area.
	dpi := int32(96)
	if procGetDpiForWindow.Find() == nil {
		// No window yet; approximate with the system DPI via a desktop probe
		// after creation. Start from 96 and let WM_DPICHANGED correct it.
	}
	cw, ch := scale(baseW, dpi), scale(baseH, dpi)
	wr := rect{0, 0, cw, ch}
	procAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&wr)), windowStyle, 0, 0)
	winW, winH := wr.Right-wr.Left, wr.Bottom-wr.Top

	var work rect
	procSystemParametersInf.Call(spiGetWork, 0, uintptr(unsafe.Pointer(&work)), 0)
	x := work.Left + (work.Right-work.Left-winW)/2
	y := work.Top + (work.Bottom-work.Top-winH)/2

	title := mustUTF16("RemoteMaster")
	hwnd, _, _ := procCreateWindowEx.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		windowStyle,
		uintptr(x), uintptr(y),
		uintptr(winW), uintptr(winH),
		0, 0, hInst, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowEx failed")
	}
	globalHwnd = hwnd

	// The window now exists on its real monitor; resize for its actual DPI.
	if real := dpiFor(hwnd); real != dpi {
		dpi = real
		cw, ch = scale(baseW, dpi), scale(baseH, dpi)
		wr = rect{0, 0, cw, ch}
		procAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&wr)), windowStyle, 0, 0)
		winW, winH = wr.Right-wr.Left, wr.Bottom-wr.Top
		x = work.Left + (work.Right-work.Left-winW)/2
		y = work.Top + (work.Bottom-work.Top-winH)/2
		procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), uintptr(winW), uintptr(winH), swpNoZOrder)
	}

	// End Session button
	btnLabel := mustUTF16("End Session")
	btnClass := mustUTF16("BUTTON")
	globalBtn, _, _ = procCreateWindowEx.Call(
		0,
		uintptr(unsafe.Pointer(btnClass)),
		uintptr(unsafe.Pointer(btnLabel)),
		wsChild|wsVisible|wsTabStop|bsFlat,
		0, 0, 10, 10,
		hwnd, btnID, hInst, 0,
	)
	layoutButton(hwnd)

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

// SetCode updates the displayed session code and redraws. Safe to call from
// any goroutine.
func SetCode(code string) {
	mu.Lock()
	globalCode = code
	globalStatus = ""
	mu.Unlock()
	if globalHwnd != 0 {
		procInvalidateRect.Call(globalHwnd, 0, 0)
	}
}

// SetStatus overrides the hint line (e.g. "Agent connected — screen is
// being shared") until the next SetCode call. Safe to call from any
// goroutine.
func SetStatus(status string) {
	mu.Lock()
	globalStatus = status
	mu.Unlock()
	if globalHwnd != 0 {
		procInvalidateRect.Call(globalHwnd, 0, 0)
	}
}
