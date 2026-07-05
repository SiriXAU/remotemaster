package relay

import (
	"bytes"
	"fmt"
	"image"
	"log"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/chai2010/webp"
)

// webpVideoEncoder converts captured frames into wire messages: full 0x01
// WebP frames and 0x0C dirty-region patches.
type webpVideoEncoder struct {
	w, h int

	// Adaptive quality: q floats between webpMinQuality and qualityCap so
	// encode time stays inside the per-frame budget (1/fps). Small dirty
	// regions encode in a millisecond or two, letting q climb back to the
	// cap; full-screen motion (window drags, video) pulls q down instead of
	// dropping the frame rate.
	q          float32
	qualityCap float32
	budget     time.Duration

	// prev holds the previously encoded frame's pixels so Encode can diff
	// against it and re-encode only the changed region. A full-screen WebP
	// encode costs hundreds of milliseconds at desktop resolutions, which
	// caps the stream at a few fps; typical desktop activity (typing, cursor
	// movement, small UI updates) touches a tiny fraction of the screen.
	prev     []byte
	lastFull time.Time
}

// webpMinQuality is the floor for adaptive quality — below this, artifacts
// on text get bad enough that a lower frame rate is the better trade.
const webpMinQuality = 30

// webpFullRefreshInterval bounds how stale the viewer's canvas can get if a
// region update is ever lost or mis-drawn. The refresh is normally sent on
// an idle tick (screen unchanged) where its full-frame encode cost is
// invisible; webpFullRefreshMaxInterval forces one even during continuous
// activity.
const (
	webpFullRefreshInterval    = 10 * time.Second
	webpFullRefreshMaxInterval = 30 * time.Second
)

// webpTileRows is the strip height at which a dirty region is split for
// parallel encoding. Regions shorter than this encode as a single strip.
const webpTileRows = 200

func newWebPVideoEncoder(w, h, fps int, quality float32) *webpVideoEncoder {
	log.Printf("video encoder: webp (dirty regions, adaptive quality, %d fps budget)", fps)
	return &webpVideoEncoder{
		w:          w,
		h:          h,
		q:          quality,
		qualityCap: quality,
		budget:     time.Second / time.Duration(max(fps, 1)),
	}
}

func (e *webpVideoEncoder) Encode(img image.Image) ([][]byte, error) {
	nrgba, ok := img.(*image.NRGBA)
	if !ok || nrgba.Stride != e.w*4 || nrgba.Rect.Dx() != e.w || nrgba.Rect.Dy() != e.h {
		// Unknown layout — encode the whole image without diffing.
		return e.encodeFull(img, nil)
	}

	if e.prev == nil || time.Since(e.lastFull) >= webpFullRefreshMaxInterval {
		return e.encodeFull(img, nrgba.Pix)
	}

	x0, y0, x1, y1 := diffBounds(e.prev, nrgba.Pix, e.w, e.h)
	if x0 > x1 {
		return nil, nil // identical frames — nothing to send
	}
	rw, rh := x1-x0+1, y1-y0+1

	// Copy the dirty rows into prev so the next diff is against what the
	// viewer now shows.
	for y := y0; y <= y1; y++ {
		row := y * e.w * 4
		copy(e.prev[row+x0*4:row+(x1+1)*4], nrgba.Pix[row+x0*4:row+(x1+1)*4])
	}

	// Large regions are split into horizontal strips encoded in parallel:
	// WebP encoding is single-threaded, so a full-screen change would
	// otherwise serialize ~70ms+ on one core while the others idle. The
	// strips are disjoint, so the viewer can draw them in any order.
	strips := rh / webpTileRows
	if strips < 1 {
		strips = 1
	}
	if maxW := runtime.GOMAXPROCS(0); strips > maxW {
		strips = maxW
	}
	if strips > 8 {
		strips = 8
	}

	start := time.Now()
	type stripResult struct {
		msg []byte
		err error
	}
	results := make([]stripResult, strips)
	var wg sync.WaitGroup
	rowsPer := (rh + strips - 1) / strips
	for i := 0; i < strips; i++ {
		sy := y0 + i*rowsPer
		sh := rowsPer
		if sy+sh > y1+1 {
			sh = y1 + 1 - sy
		}
		if sh <= 0 {
			continue
		}
		wg.Add(1)
		go func(i, sy, sh int) {
			defer wg.Done()
			crop := image.NewNRGBA(image.Rect(0, 0, rw, sh))
			for y := 0; y < sh; y++ {
				srcRow := (sy+y)*e.w*4 + x0*4
				copy(crop.Pix[y*crop.Stride:y*crop.Stride+rw*4], nrgba.Pix[srcRow:srcRow+rw*4])
			}
			data, err := webp.EncodeRGBA(crop, e.q)
			if err == nil && len(data) == 0 {
				err = fmt.Errorf("empty strip")
			}
			if err != nil {
				results[i].err = err
				return
			}
			results[i].msg = encodeRegionFrame(x0, sy, rw, sh, data)
		}(i, sy, sh)
	}
	wg.Wait()
	e.adapt(time.Since(start))

	var messages [][]byte
	for _, r := range results {
		if r.err != nil {
			return nil, fmt.Errorf("webp region encode: %w", r.err)
		}
		if r.msg != nil {
			messages = append(messages, r.msg)
		}
	}
	return messages, nil
}

// adapt is the quality feedback loop: each encode's duration nudges q so the
// encode cost converges under the per-frame budget. Down-steps are larger
// than up-steps so a burst of full-screen motion sheds quality quickly and
// recovers it gradually once the workload lightens.
func (e *webpVideoEncoder) adapt(d time.Duration) {
	switch {
	case d > e.budget*8/10:
		e.q -= 8
		if e.q < webpMinQuality {
			e.q = webpMinQuality
		}
	case d < e.budget*4/10 && e.q < e.qualityCap:
		e.q += 4
		if e.q > e.qualityCap {
			e.q = e.qualityCap
		}
	}
}

// encodeFull sends the whole frame and, when pix is the frame's raw pixels,
// snapshots it for subsequent region diffs.
func (e *webpVideoEncoder) encodeFull(img image.Image, pix []byte) ([][]byte, error) {
	start := time.Now()
	data, err := webp.EncodeRGBA(img, e.q)
	e.adapt(time.Since(start))
	if err != nil {
		return nil, fmt.Errorf("webp encode: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("webp encode: empty frame")
	}
	if pix != nil {
		if e.prev == nil {
			e.prev = make([]byte, len(pix))
		}
		copy(e.prev, pix)
		e.lastFull = time.Now()
	} else {
		e.prev = nil
	}
	return [][]byte{encodeFrame(e.w, e.h, data)}, nil
}

// diffBounds returns the bounding box (inclusive) of pixels that differ
// between two same-layout RGBA buffers, or x0>x1 when they are identical.
func diffBounds(prev, cur []byte, w, h int) (x0, y0, x1, y1 int) {
	x0, y0 = w, h
	x1, y1 = -1, -1
	stride := w * 4
	for y := 0; y < h; y++ {
		row := y * stride
		if bytes.Equal(prev[row:row+stride], cur[row:row+stride]) {
			continue
		}
		if y0 == h {
			y0 = y
		}
		y1 = y
		// Tighten the horizontal bounds. Only the spans outside the current
		// box need scanning, so the per-row cost shrinks as the box grows.
		for x := 0; x < x0; x++ {
			o := row + x*4
			if *(*uint32)(unsafe.Pointer(&prev[o])) != *(*uint32)(unsafe.Pointer(&cur[o])) {
				x0 = x
				break
			}
		}
		for x := w - 1; x > x1; x-- {
			o := row + x*4
			if *(*uint32)(unsafe.Pointer(&prev[o])) != *(*uint32)(unsafe.Pointer(&cur[o])) {
				x1 = x
				break
			}
		}
	}
	if y1 < 0 {
		return 1, 1, 0, 0
	}
	return x0, y0, x1, y1
}

// CurrentQuality exposes the adaptive quality level for stats logging.
func (e *webpVideoEncoder) CurrentQuality() float32 { return e.q }

// IdleTick runs on ticks where the screen didn't change. That idle moment is
// the cheap place to send the periodic full refresh: the encode cost
// stutters nothing because no motion is on screen, and it repaints at the
// quality cap (idle encodes have long since restored q) so the desktop
// sharpens right after activity stops.
func (e *webpVideoEncoder) IdleTick() [][]byte {
	if e.prev == nil || time.Since(e.lastFull) < webpFullRefreshInterval {
		return nil
	}
	img := &image.NRGBA{
		Pix:    e.prev,
		Stride: e.w * 4,
		Rect:   image.Rect(0, 0, e.w, e.h),
	}
	msgs, err := e.encodeFull(img, e.prev)
	if err != nil {
		return nil
	}
	return msgs
}

func (e *webpVideoEncoder) Close() error {
	return nil
}
