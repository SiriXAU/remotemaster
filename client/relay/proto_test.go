package relay

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/sirixau/remotemaster/client/input"
)

func TestEncodeFrame(t *testing.T) {
	payload := []byte{1, 2, 3}
	frame := encodeFrame(1920, 1080, payload)

	if frame[0] != binFrame {
		t.Fatalf("message type = %d, want %d", frame[0], binFrame)
	}
	if got := binary.BigEndian.Uint32(frame[1:5]); got != 1920 {
		t.Fatalf("width = %d, want 1920", got)
	}
	if got := binary.BigEndian.Uint32(frame[5:9]); got != 1080 {
		t.Fatalf("height = %d, want 1080", got)
	}
	if !bytes.Equal(frame[9:], payload) {
		t.Fatalf("payload = %v, want %v", frame[9:], payload)
	}
}

func TestDecodeEvent(t *testing.T) {
	raw := []byte{binMouseDown, 0x01, 0x2c, 0x00, 0x64, 0x02}
	ev, ok := decodeEvent(raw)
	if !ok {
		t.Fatal("decodeEvent returned false")
	}
	if ev.Type != input.TypeMouseDown || ev.X != 300 || ev.Y != 100 || ev.Btn != "right" {
		t.Fatalf("event = %#v", ev)
	}
}

func TestClipboardRoundTrip(t *testing.T) {
	for _, text := range []string{"", "hello", "multi\nline\ntext", "unicode: héllo wörld 🎉"} {
		msg := encodeClipboard(text)
		if msg[0] != binClipboard {
			t.Fatalf("message type = %d, want %d", msg[0], binClipboard)
		}
		got, ok := decodeClipboard(msg)
		if !ok || got != text {
			t.Fatalf("decodeClipboard(encodeClipboard(%q)) = %q, %v", text, got, ok)
		}
	}
}

func TestDecodeClipboardRejects(t *testing.T) {
	if _, ok := decodeClipboard(nil); ok {
		t.Fatal("decoded empty message")
	}
	if _, ok := decodeClipboard([]byte{binFrame, 'h', 'i'}); ok {
		t.Fatal("decoded message with wrong tag")
	}
	huge := make([]byte, 1+maxClipboardBytes+1)
	huge[0] = binClipboard
	if _, ok := decodeClipboard(huge); ok {
		t.Fatal("decoded oversized clipboard message")
	}
	exact := make([]byte, 1+maxClipboardBytes)
	exact[0] = binClipboard
	if _, ok := decodeClipboard(exact); !ok {
		t.Fatal("rejected clipboard message at the size limit")
	}
}
