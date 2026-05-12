//go:build windows

package capture

import (
	"fmt"
	"image"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modUser32 = windows.NewLazyDLL("user32.dll")
	modGDI32  = windows.NewLazyDLL("gdi32.dll")

	procGetDC               = modUser32.NewProc("GetDC")
	procReleaseDC           = modUser32.NewProc("ReleaseDC")
	procGetSystemMetrics    = modUser32.NewProc("GetSystemMetrics")
	procCreateCompatibleDC  = modGDI32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBmp = modGDI32.NewProc("CreateCompatibleBitmap")
	procSelectObject        = modGDI32.NewProc("SelectObject")
	procBitBlt              = modGDI32.NewProc("BitBlt")
	procDeleteDC            = modGDI32.NewProc("DeleteDC")
	procDeleteObject        = modGDI32.NewProc("DeleteObject")
	procGetDIBits           = modGDI32.NewProc("GetDIBits")
)

const (
	smCxScreen    = 0
	smCyScreen    = 1
	srccopy       = 0x00CC0020
	captureBlt    = 0x40000000
	dibRgbBitmaps = 0
	biRgb         = 0
)

type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type bitmapInfo struct {
	BmiHeader bitmapInfoHeader
}

// GDICapturer captures screens using GDI BitBlt (no CGo required).
type GDICapturer struct {
	mu   sync.Mutex
	w, h int
}

// New returns a new GDICapturer. Call Close when done.
func New() (*GDICapturer, error) {
	w, _, _ := procGetSystemMetrics.Call(smCxScreen)
	h, _, _ := procGetSystemMetrics.Call(smCyScreen)
	if w == 0 || h == 0 {
		return nil, fmt.Errorf("could not query screen metrics")
	}
	return &GDICapturer{w: int(w), h: int(h)}, nil
}

func (c *GDICapturer) Bounds() (int, int) { return c.w, c.h }

func (c *GDICapturer) Close() {}

// Capture takes a screenshot of the primary monitor and returns an NRGBA image.
func (c *GDICapturer) Capture() (image.Image, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("GetDC failed")
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(memDC)

	bmp, _, _ := procCreateCompatibleBmp.Call(screenDC, uintptr(c.w), uintptr(c.h))
	if bmp == 0 {
		return nil, fmt.Errorf("CreateCompatibleBitmap failed")
	}
	defer procDeleteObject.Call(bmp)

	procSelectObject.Call(memDC, bmp)

	ret, _, _ := procBitBlt.Call(
		memDC, 0, 0, uintptr(c.w), uintptr(c.h),
		screenDC, 0, 0,
		srccopy|captureBlt,
	)
	if ret == 0 {
		return nil, fmt.Errorf("BitBlt failed")
	}

	bih := bitmapInfoHeader{
		BiSize:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		BiWidth:       int32(c.w),
		BiHeight:      -int32(c.h), // negative = top-down
		BiPlanes:      1,
		BiBitCount:    32,
		BiCompression: biRgb,
	}
	bi := bitmapInfo{BmiHeader: bih}
	pixels := make([]byte, c.w*c.h*4)

	ret, _, _ = procGetDIBits.Call(
		screenDC,
		bmp,
		0,
		uintptr(c.h),
		uintptr(unsafe.Pointer(&pixels[0])),
		uintptr(unsafe.Pointer(&bi)),
		dibRgbBitmaps,
	)
	if ret == 0 {
		return nil, fmt.Errorf("GetDIBits failed")
	}

	// GDI returns BGRA; convert to RGBA for image.NRGBA using 32-bit swaps.
	// Each iteration handles one pixel via uint32 bit manipulation instead of
	// four byte operations, roughly halving memory traffic.
	img := image.NewNRGBA(image.Rect(0, 0, c.w, c.h))
	for i := 0; i < len(pixels); i += 4 {
		bgra := *(*uint32)(unsafe.Pointer(&pixels[i]))
		rgba := (bgra & 0xFF00FF00) | ((bgra & 0xFF) << 16) | ((bgra >> 16) & 0xFF) | 0xFF000000
		*(*uint32)(unsafe.Pointer(&img.Pix[i])) = rgba
	}
	return img, nil
}
