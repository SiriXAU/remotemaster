package relay

import (
	"encoding/binary"
	"fmt"

	"github.com/sirixau/remotemaster/client/input"
)

// Binary protocol message types.
const (
	binFrame     = 0x01
	binMouseMove = 0x02
	binMouseDown = 0x03
	binMouseUp   = 0x04
	binScroll    = 0x05
	binKeyDown   = 0x06
	binKeyUp     = 0x07
	binClipboard = 0x0A
	// binRegionFrame carries a WebP-encoded sub-rectangle of the screen:
	// [type:1][x:u32][y:u32][w:u32][h:u32][webp]. The viewer draws it at
	// (x,y) on top of the last full binFrame. Tags 0x08/0x09/0x0B belonged
	// to the removed encoded-video (H.264) path and stay reserved.
	binRegionFrame = 0x0C
)

// maxClipboardBytes caps clipboard payloads in both directions so a huge
// copy (e.g. a file's contents) cannot stall the frame channel.
const maxClipboardBytes = 256 * 1024

// btnCodes maps agent button strings to binary protocol values.
var btnToCode = map[string]byte{"left": 0, "middle": 1, "right": 2}
var codeToBtn = map[byte]string{0: "left", 1: "middle", 2: "right"}

func encodeFrame(w, h int, data []byte) []byte {
	buf := make([]byte, 9+len(data))
	buf[0] = binFrame
	binary.BigEndian.PutUint32(buf[1:5], uint32(w))
	binary.BigEndian.PutUint32(buf[5:9], uint32(h))
	copy(buf[9:], data)
	return buf
}

func encodeRegionFrame(x, y, w, h int, data []byte) []byte {
	buf := make([]byte, 17+len(data))
	buf[0] = binRegionFrame
	binary.BigEndian.PutUint32(buf[1:5], uint32(x))
	binary.BigEndian.PutUint32(buf[5:9], uint32(y))
	binary.BigEndian.PutUint32(buf[9:13], uint32(w))
	binary.BigEndian.PutUint32(buf[13:17], uint32(h))
	copy(buf[17:], data)
	return buf
}

// encodeClipboard packs clipboard text as [0x0A][utf8 bytes].
func encodeClipboard(text string) []byte {
	b := make([]byte, 1+len(text))
	b[0] = binClipboard
	copy(b[1:], text)
	return b
}

// decodeClipboard unpacks a clipboard message, rejecting oversized payloads.
func decodeClipboard(p []byte) (string, bool) {
	if len(p) < 1 || p[0] != binClipboard || len(p)-1 > maxClipboardBytes {
		return "", false
	}
	return string(p[1:]), true
}

func decodeEvent(p []byte) (input.Event, bool) {
	if len(p) < 1 {
		return input.Event{}, false
	}
	switch p[0] {
	case binMouseMove:
		if len(p) < 5 {
			return input.Event{}, false
		}
		return input.Event{
			Type: input.TypeMouseMove,
			X:    int(binary.BigEndian.Uint16(p[1:3])),
			Y:    int(binary.BigEndian.Uint16(p[3:5])),
		}, true
	case binMouseDown:
		if len(p) < 6 {
			return input.Event{}, false
		}
		return input.Event{
			Type: input.TypeMouseDown,
			X:    int(binary.BigEndian.Uint16(p[1:3])),
			Y:    int(binary.BigEndian.Uint16(p[3:5])),
			Btn:  codeToBtn[p[5]],
		}, true
	case binMouseUp:
		if len(p) < 6 {
			return input.Event{}, false
		}
		return input.Event{
			Type: input.TypeMouseUp,
			X:    int(binary.BigEndian.Uint16(p[1:3])),
			Y:    int(binary.BigEndian.Uint16(p[3:5])),
			Btn:  codeToBtn[p[5]],
		}, true
	case binScroll:
		if len(p) < 9 {
			return input.Event{}, false
		}
		return input.Event{
			Type: input.TypeScroll,
			X:    int(binary.BigEndian.Uint16(p[1:3])),
			Y:    int(binary.BigEndian.Uint16(p[3:5])),
			Dx:   int(int16(binary.BigEndian.Uint16(p[5:7]))),
			Dy:   int(int16(binary.BigEndian.Uint16(p[7:9]))),
		}, true
	case binKeyDown:
		if len(p) < 3 {
			return input.Event{}, false
		}
		return input.Event{
			Type: input.TypeKeyDown,
			VK:   int(binary.BigEndian.Uint16(p[1:3])),
		}, true
	case binKeyUp:
		if len(p) < 3 {
			return input.Event{}, false
		}
		return input.Event{
			Type: input.TypeKeyUp,
			VK:   int(binary.BigEndian.Uint16(p[1:3])),
		}, true
	}
	return input.Event{}, false
}

// encodeEventMirror encodes an event for the agent→client direction in the relay.
// This is the inverse of decodeEvent and uses the same binary protocol.
func encodeEventMirror(e input.Event) ([]byte, error) {
	switch e.Type {
	case input.TypeMouseMove:
		b := make([]byte, 5)
		b[0] = binMouseMove
		binary.BigEndian.PutUint16(b[1:3], uint16(e.X))
		binary.BigEndian.PutUint16(b[3:5], uint16(e.Y))
		return b, nil
	case input.TypeMouseDown:
		b := make([]byte, 6)
		b[0] = binMouseDown
		binary.BigEndian.PutUint16(b[1:3], uint16(e.X))
		binary.BigEndian.PutUint16(b[3:5], uint16(e.Y))
		b[5] = btnToCode[e.Btn]
		return b, nil
	case input.TypeMouseUp:
		b := make([]byte, 6)
		b[0] = binMouseUp
		binary.BigEndian.PutUint16(b[1:3], uint16(e.X))
		binary.BigEndian.PutUint16(b[3:5], uint16(e.Y))
		b[5] = btnToCode[e.Btn]
		return b, nil
	case input.TypeScroll:
		b := make([]byte, 9)
		b[0] = binScroll
		binary.BigEndian.PutUint16(b[1:3], uint16(e.X))
		binary.BigEndian.PutUint16(b[3:5], uint16(e.Y))
		binary.BigEndian.PutUint16(b[5:7], uint16(int16(e.Dx)))
		binary.BigEndian.PutUint16(b[7:9], uint16(int16(e.Dy)))
		return b, nil
	case input.TypeKeyDown:
		b := make([]byte, 3)
		b[0] = binKeyDown
		binary.BigEndian.PutUint16(b[1:3], uint16(e.VK))
		return b, nil
	case input.TypeKeyUp:
		b := make([]byte, 3)
		b[0] = binKeyUp
		binary.BigEndian.PutUint16(b[1:3], uint16(e.VK))
		return b, nil
	}
	return nil, fmt.Errorf("unknown event type %q", e.Type)
}
