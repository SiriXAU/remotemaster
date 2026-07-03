//go:build windows

package clipboard

import (
	"fmt"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procOpenClipboard              = modUser32.NewProc("OpenClipboard")
	procCloseClipboard             = modUser32.NewProc("CloseClipboard")
	procEmptyClipboard             = modUser32.NewProc("EmptyClipboard")
	procGetClipboardData           = modUser32.NewProc("GetClipboardData")
	procSetClipboardData           = modUser32.NewProc("SetClipboardData")
	procIsClipboardFormatAvailable = modUser32.NewProc("IsClipboardFormatAvailable")

	procGlobalAlloc  = modKernel32.NewProc("GlobalAlloc")
	procGlobalFree   = modKernel32.NewProc("GlobalFree")
	procGlobalLock   = modKernel32.NewProc("GlobalLock")
	procGlobalUnlock = modKernel32.NewProc("GlobalUnlock")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

// WindowsClipboard implements Clipboard via the Win32 clipboard API.
type WindowsClipboard struct{}

func New() (*WindowsClipboard, error) {
	return &WindowsClipboard{}, nil
}

// open retries briefly because the clipboard is a single system-wide resource
// that any application can hold open.
func (c *WindowsClipboard) open() error {
	for range 5 {
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("clipboard held by another application")
}

func (c *WindowsClipboard) GetText() (string, error) {
	if err := c.open(); err != nil {
		return "", err
	}
	defer procCloseClipboard.Call()

	if avail, _, _ := procIsClipboardFormatAvailable.Call(cfUnicodeText); avail == 0 {
		return "", nil
	}
	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", fmt.Errorf("GetClipboardData failed")
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return "", fmt.Errorf("GlobalLock failed")
	}
	defer procGlobalUnlock.Call(h)

	// vet flags uintptr→Pointer conversions, but GlobalLock returns a real
	// pointer that stays valid until GlobalUnlock — the same pattern as
	// x/sys/windows' generated syscall wrappers.
	text := windows.UTF16PtrToString((*uint16)(unsafe.Pointer(p))) //nolint:govet
	// Normalize Windows line endings to \n for the wire.
	return strings.ReplaceAll(text, "\r\n", "\n"), nil
}

func (c *WindowsClipboard) SetText(text string) error {
	// Windows applications expect CRLF; the wire format uses bare \n.
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
	// CF_UNICODETEXT is NUL-terminated, so interior NULs cannot round-trip.
	text = strings.ReplaceAll(text, "\x00", "")

	u16, err := windows.UTF16FromString(text)
	if err != nil {
		return fmt.Errorf("utf16 convert: %w", err)
	}

	if err := c.open(); err != nil {
		return err
	}
	defer procCloseClipboard.Call()

	procEmptyClipboard.Call()

	size := uintptr(len(u16) * 2)
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return fmt.Errorf("GlobalAlloc failed")
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return fmt.Errorf("GlobalLock failed")
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(p)), len(u16)), u16) //nolint:govet // see GetText
	procGlobalUnlock.Call(h)

	// On success the system owns the memory; free it only on failure.
	if r, _, _ := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		procGlobalFree.Call(h)
		return fmt.Errorf("SetClipboardData failed")
	}
	return nil
}
