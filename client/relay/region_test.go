package relay

import (
	"encoding/binary"
	"image"
	"testing"
	"time"
)

func TestDiffBounds(t *testing.T) {
	const w, h = 16, 12
	prev := make([]byte, w*h*4)
	cur := make([]byte, w*h*4)

	if x0, _, x1, _ := diffBounds(prev, cur, w, h); x0 <= x1 {
		t.Fatalf("identical buffers reported dirty box (%d..%d)", x0, x1)
	}

	set := func(x, y int) { cur[(y*w+x)*4+1] = 0xFF }
	set(3, 2)
	set(10, 7)

	x0, y0, x1, y1 := diffBounds(prev, cur, w, h)
	if x0 != 3 || y0 != 2 || x1 != 10 || y1 != 7 {
		t.Fatalf("got box (%d,%d)-(%d,%d), want (3,2)-(10,7)", x0, y0, x1, y1)
	}
}

func TestWebPEncoderRegionFrames(t *testing.T) {
	const w, h = 64, 48
	enc := newWebPVideoEncoder(w, h, 25, 80).(*webpVideoEncoder)
	img := image.NewNRGBA(image.Rect(0, 0, w, h))

	// First frame is always a full 0x01 frame and primes the diff buffer.
	msgs, err := enc.Encode(img)
	if err != nil {
		t.Fatalf("first encode: %v", err)
	}
	if len(msgs) != 1 || msgs[0][0] != binFrame {
		t.Fatalf("first frame: got type %#02x, want binFrame", msgs[0][0])
	}

	// An identical frame produces nothing.
	msgs, err = enc.Encode(img)
	if err != nil || len(msgs) != 0 {
		t.Fatalf("identical frame: msgs=%d err=%v, want none", len(msgs), err)
	}

	// A small change produces a region frame with the right box.
	img.Pix[(10*w+20)*4] = 0xFF
	msgs, err = enc.Encode(img)
	if err != nil {
		t.Fatalf("region encode: %v", err)
	}
	if len(msgs) != 1 || msgs[0][0] != binRegionFrame {
		t.Fatalf("region frame: got type %#02x, want binRegionFrame", msgs[0][0])
	}
	x := binary.BigEndian.Uint32(msgs[0][1:5])
	y := binary.BigEndian.Uint32(msgs[0][5:9])
	rw := binary.BigEndian.Uint32(msgs[0][9:13])
	rh := binary.BigEndian.Uint32(msgs[0][13:17])
	if x != 20 || y != 10 || rw != 1 || rh != 1 {
		t.Fatalf("region box = (%d,%d %dx%d), want (20,10 1x1)", x, y, rw, rh)
	}

	// The same frame again is clean — prev was updated with the region.
	msgs, err = enc.Encode(img)
	if err != nil || len(msgs) != 0 {
		t.Fatalf("post-region identical frame: msgs=%d err=%v, want none", len(msgs), err)
	}

	// An idle tick past the refresh interval emits a full-frame refresh.
	enc.lastFull = time.Now().Add(-webpFullRefreshInterval - time.Second)
	if msgs := enc.DrainPackets(); len(msgs) != 1 || msgs[0][0] != binFrame {
		t.Fatalf("idle refresh: got %d msgs, want one binFrame", len(msgs))
	}
	if enc.DrainPackets() != nil {
		t.Fatalf("idle refresh should reset the interval")
	}

	// After the hard cap a full frame is sent even for small changes.
	enc.lastFull = time.Now().Add(-webpFullRefreshMaxInterval - time.Second)
	img.Pix[0] = 0xFF
	msgs, err = enc.Encode(img)
	if err != nil {
		t.Fatalf("refresh encode: %v", err)
	}
	if len(msgs) != 1 || msgs[0][0] != binFrame {
		t.Fatalf("refresh frame: got type %#02x, want binFrame", msgs[0][0])
	}
}
