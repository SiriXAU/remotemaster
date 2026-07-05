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
	procGetCursorInfo       = modUser32.NewProc("GetCursorInfo")
	procGetIconInfo         = modUser32.NewProc("GetIconInfo")
	procDrawIconEx          = modUser32.NewProc("DrawIconEx")
)

const (
	smCxScreen    = 0
	smCyScreen    = 1
	srccopy       = 0x00CC0020
	captureBlt    = 0x40000000
	dibRgbBitmaps = 0
	biRgb         = 0

	cursorShowing = 0x0001
	diNormal      = 0x0003
)

type point struct {
	X, Y int32
}

type cursorInfo struct {
	CbSize      uint32
	Flags       uint32
	HCursor     uintptr
	PtScreenPos point
}

type iconInfo struct {
	FIcon    int32
	XHotspot uint32
	YHotspot uint32
	HbmMask  uintptr
	HbmColor uintptr
}

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
//
// The GDI device contexts, the compatible bitmap, the DIB pixel buffer, and the
// destination image are allocated once and reused across every Capture call.
// Recreating them per frame (as an earlier version did) cost several kernel
// round-trips and ~2×frame-size heap allocations at the target frame rate.
type GDICapturer struct {
	mu   sync.Mutex
	w, h int

	initialized bool
	screenDC    uintptr
	memDC       uintptr
	bmp         uintptr
	oldObj      uintptr
	pixels      []byte
	img         *image.NRGBA
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

// Close releases the cached GDI resources. Safe to call more than once.
func (c *GDICapturer) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.release()
}

// release frees the cached GDI handles. The caller must hold c.mu.
func (c *GDICapturer) release() {
	if !c.initialized {
		return
	}
	if c.memDC != 0 {
		if c.oldObj != 0 {
			procSelectObject.Call(c.memDC, c.oldObj)
		}
		procDeleteDC.Call(c.memDC)
	}
	if c.bmp != 0 {
		procDeleteObject.Call(c.bmp)
	}
	if c.screenDC != 0 {
		procReleaseDC.Call(0, c.screenDC)
	}
	c.screenDC, c.memDC, c.bmp, c.oldObj = 0, 0, 0, 0
	c.pixels, c.img = nil, nil
	c.initialized = false
}

// ensure lazily creates the reusable GDI context, bitmap, and buffers. The
// caller must hold c.mu.
func (c *GDICapturer) ensure() error {
	if c.initialized {
		return nil
	}

	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return fmt.Errorf("GetDC failed")
	}
	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		procReleaseDC.Call(0, screenDC)
		return fmt.Errorf("CreateCompatibleDC failed")
	}
	bmp, _, _ := procCreateCompatibleBmp.Call(screenDC, uintptr(c.w), uintptr(c.h))
	if bmp == 0 {
		procDeleteDC.Call(memDC)
		procReleaseDC.Call(0, screenDC)
		return fmt.Errorf("CreateCompatibleBitmap failed")
	}
	oldObj, _, _ := procSelectObject.Call(memDC, bmp)

	c.screenDC = screenDC
	c.memDC = memDC
	c.bmp = bmp
	c.oldObj = oldObj
	c.pixels = make([]byte, c.w*c.h*4)
	c.img = image.NewNRGBA(image.Rect(0, 0, c.w, c.h))
	c.initialized = true
	return nil
}

// Capture takes a screenshot of the primary monitor and returns an NRGBA image.
//
// The returned image reuses an internal buffer and is only valid until the next
// Capture call — callers must finish reading (encoding/hashing) it before
// capturing again. The existing single-goroutine capture loop satisfies this.
func (c *GDICapturer) Capture() (image.Image, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensure(); err != nil {
		return nil, err
	}

	ret, _, _ := procBitBlt.Call(
		c.memDC, 0, 0, uintptr(c.w), uintptr(c.h),
		c.screenDC, 0, 0,
		srccopy|captureBlt,
	)
	if ret == 0 {
		// A failed BitBlt can mean the display context was invalidated (e.g. a
		// resolution change or session switch); drop the cache so the next call
		// rebuilds it.
		c.release()
		return nil, fmt.Errorf("BitBlt failed")
	}

	// BitBlt does not include the mouse cursor; composite it onto the frame so
	// the viewer can see where the pointer is. Best effort — a failure here
	// still yields a usable (cursor-less) frame.
	c.drawCursor()

	bih := bitmapInfoHeader{
		BiSize:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		BiWidth:       int32(c.w),
		BiHeight:      -int32(c.h), // negative = top-down
		BiPlanes:      1,
		BiBitCount:    32,
		BiCompression: biRgb,
	}
	bi := bitmapInfo{BmiHeader: bih}

	ret, _, _ = procGetDIBits.Call(
		c.screenDC,
		c.bmp,
		0,
		uintptr(c.h),
		uintptr(unsafe.Pointer(&c.pixels[0])),
		uintptr(unsafe.Pointer(&bi)),
		dibRgbBitmaps,
	)
	if ret == 0 {
		return nil, fmt.Errorf("GetDIBits failed")
	}

	// GDI returns BGRA; convert to RGBA for image.NRGBA using 32-bit swaps.
	// Each iteration handles one pixel via uint32 bit manipulation instead of
	// four byte operations, roughly halving memory traffic.
	pixels := c.pixels
	dst := c.img.Pix
	for i := 0; i < len(pixels); i += 4 {
		bgra := *(*uint32)(unsafe.Pointer(&pixels[i]))
		rgba := (bgra & 0xFF00FF00) | ((bgra & 0xFF) << 16) | ((bgra >> 16) & 0xFF) | 0xFF000000
		*(*uint32)(unsafe.Pointer(&dst[i])) = rgba
	}
	return c.img, nil
}

// drawCursor composites the current mouse cursor into the memory DC at its
// on-screen position, hotspot-adjusted. The caller must hold c.mu.
func (c *GDICapturer) drawCursor() {
	var ci cursorInfo
	ci.CbSize = uint32(unsafe.Sizeof(ci))
	if ret, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci))); ret == 0 {
		return
	}
	if ci.Flags&cursorShowing == 0 || ci.HCursor == 0 {
		return
	}

	var hotX, hotY int32
	var ii iconInfo
	if ret, _, _ := procGetIconInfo.Call(ci.HCursor, uintptr(unsafe.Pointer(&ii))); ret != 0 {
		hotX, hotY = int32(ii.XHotspot), int32(ii.YHotspot)
		// GetIconInfo hands out copies of the cursor bitmaps; free them or
		// they leak once per frame.
		if ii.HbmMask != 0 {
			procDeleteObject.Call(ii.HbmMask)
		}
		if ii.HbmColor != 0 {
			procDeleteObject.Call(ii.HbmColor)
		}
	}

	procDrawIconEx.Call(
		c.memDC,
		uintptr(ci.PtScreenPos.X-hotX),
		uintptr(ci.PtScreenPos.Y-hotY),
		ci.HCursor,
		0, 0, 0, 0,
		diNormal,
	)
}
