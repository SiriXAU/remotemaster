//go:build windows

package input

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modUser32     = windows.NewLazyDLL("user32.dll")
	procSendInput = modUser32.NewProc("SendInput")
	procGetSMX    = modUser32.NewProc("GetSystemMetrics")
)

const (
	inputMouse    = 0
	inputKeyboard = 1

	mouseMoveAbs    = 0x0001 | 0x8000 // MOUSEEVENTF_MOVE | MOUSEEVENTF_ABSOLUTE
	mouseLeftDown   = 0x0002
	mouseLeftUp     = 0x0004
	mouseRightDown  = 0x0008
	mouseRightUp    = 0x0010
	mouseMiddleDown = 0x0020
	mouseMiddleUp   = 0x0040
	mouseWheel      = 0x0800
	mouseHWheel     = 0x01000

	keyDown     = 0x0000
	keyUp       = 0x0002
	extendedKey = 0x0001

	smCxScreen = 0
	smCyScreen = 1
)

// mouseInput mirrors the Win32 MOUSEINPUT structure.
type mouseInput struct {
	Dx          int32
	Dy          int32
	MouseData   uint32
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

// keybdInput mirrors Win32 KEYBDINPUT.
type keybdInput struct {
	WVk         uint16
	WScan       uint16
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
	_           [8]byte // padding to match union size on 64-bit
}

// input mirrors Win32 INPUT (type + union, padded to 40 bytes on x64).
type inputUnion struct {
	Type uint32
	_    uint32 // alignment padding
	Mi   mouseInput
}

type inputKeyUnion struct {
	Type uint32
	_    uint32
	Ki   keybdInput
}

// WindowsInjector injects mouse and keyboard events using SendInput.
type WindowsInjector struct {
	screenW, screenH int
}

func NewInjector() (*WindowsInjector, error) {
	w, _, _ := procGetSMX.Call(smCxScreen)
	h, _, _ := procGetSMX.Call(smCyScreen)
	if w == 0 || h == 0 {
		return nil, fmt.Errorf("could not get primary screen size")
	}
	return &WindowsInjector{screenW: int(w), screenH: int(h)}, nil
}

func (inj *WindowsInjector) Inject(e Event) error {
	switch e.Type {
	case TypeMouseMove:
		return inj.mouseEvent(e.X, e.Y, mouseMoveAbs, 0)
	case TypeMouseDown:
		if err := inj.mouseEvent(e.X, e.Y, mouseMoveAbs, 0); err != nil {
			return err
		}
		return inj.mouseEvent(e.X, e.Y, inj.downFlag(e.Btn), 0)
	case TypeMouseUp:
		if err := inj.mouseEvent(e.X, e.Y, mouseMoveAbs, 0); err != nil {
			return err
		}
		return inj.mouseEvent(e.X, e.Y, inj.upFlag(e.Btn), 0)
	case TypeScroll:
		if err := inj.mouseEvent(e.X, e.Y, mouseMoveAbs, 0); err != nil {
			return err
		}
		if e.Dy != 0 {
			if err := inj.mouseEvent(e.X, e.Y, mouseWheel, uint32(e.Dy*-120)); err != nil {
				return err
			}
		}
		if e.Dx != 0 {
			if err := inj.mouseEvent(e.X, e.Y, mouseHWheel, uint32(e.Dx*120)); err != nil {
				return err
			}
		}
		return nil
	case TypeKeyDown:
		return inj.keyEvent(uint16(e.VK), keyDown)
	case TypeKeyUp:
		return inj.keyEvent(uint16(e.VK), keyUp)
	}
	return nil
}

func (inj *WindowsInjector) mouseEvent(x, y int, flags, data uint32) error {
	// Scale x,y (in remote pixel coords) to absolute 0-65535 range.
	x = clamp(x, 0, inj.screenW-1)
	y = clamp(y, 0, inj.screenH-1)
	denomX := max(inj.screenW-1, 1)
	denomY := max(inj.screenH-1, 1)
	absX := int32(x * 65535 / denomX)
	absY := int32(y * 65535 / denomY)

	inp := inputUnion{
		Type: inputMouse,
		Mi: mouseInput{
			Dx:        absX,
			Dy:        absY,
			MouseData: data,
			DwFlags:   flags,
		},
	}
	ret, _, err := procSendInput.Call(1, uintptr(unsafe.Pointer(&inp)), unsafe.Sizeof(inp))
	if ret == 0 {
		return fmt.Errorf("SendInput mouse: %w", err)
	}
	return nil
}

func clamp(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func (inj *WindowsInjector) keyEvent(vk uint16, flags uint32) error {
	inp := inputKeyUnion{
		Type: inputKeyboard,
		Ki: keybdInput{
			WVk:     vk,
			DwFlags: flags,
		},
	}
	ret, _, err := procSendInput.Call(1, uintptr(unsafe.Pointer(&inp)), unsafe.Sizeof(inp))
	if ret == 0 {
		return fmt.Errorf("SendInput key: %w", err)
	}
	return nil
}

func (inj *WindowsInjector) downFlag(btn string) uint32 {
	switch btn {
	case "right":
		return mouseRightDown
	case "middle":
		return mouseMiddleDown
	default:
		return mouseLeftDown
	}
}

func (inj *WindowsInjector) upFlag(btn string) uint32 {
	switch btn {
	case "right":
		return mouseRightUp
	case "middle":
		return mouseMiddleUp
	default:
		return mouseLeftUp
	}
}
