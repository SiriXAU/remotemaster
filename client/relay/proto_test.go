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

func TestVideoConfigRoundTrip(t *testing.T) {
	desc := []byte{0x01, 0x64, 0x00, 0x1f}
	raw, err := encodeVideoConfig(2560, 1440, "avc1.64001f", desc)
	if err != nil {
		t.Fatalf("encodeVideoConfig: %v", err)
	}

	got, ok := decodeVideoConfig(raw)
	if !ok {
		t.Fatal("decodeVideoConfig returned false")
	}
	if got.W != 2560 || got.H != 1440 || got.Codec != "avc1.64001f" {
		t.Fatalf("config = %#v", got)
	}
	if !bytes.Equal(got.Description, desc) {
		t.Fatalf("description = %v, want %v", got.Description, desc)
	}
}

func TestEncodeVideoConfigRejectsUnknownCodec(t *testing.T) {
	if _, err := encodeVideoConfig(1920, 1080, "vp09.00.10.08", nil); err == nil {
		t.Fatal("encodeVideoConfig accepted an unsupported codec")
	}
}

func TestVideoFormatFromCodecString(t *testing.T) {
	tests := []struct {
		name   string
		codec  string
		want   VideoFormat
		wantOK bool
	}{
		{
			name:   "h264",
			codec:  "avc1.64001f",
			want:   VideoFormatH264,
			wantOK: true,
		},
		{
			name:   "hevc-main",
			codec:  "hvc1.1.6.L120.90",
			want:   VideoFormatH265,
			wantOK: true,
		},
		{
			name:   "hevc-main10",
			codec:  "hev1.2.4.L120.B0",
			want:   VideoFormatH265Main10,
			wantOK: true,
		},
		{
			name:   "av1-main8",
			codec:  "av01.0.08M.08",
			want:   VideoFormatAV1Main8,
			wantOK: true,
		},
		{
			name:   "av1-high10-444",
			codec:  "av01.1.12M.10",
			want:   VideoFormatAV1High10444,
			wantOK: true,
		},
		{
			name:   "unsupported",
			codec:  "vp09.00.10.08",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := videoFormatFromCodecString(tt.codec)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got != tt.want {
				t.Fatalf("format = %#04x, want %#04x", got, tt.want)
			}
		})
	}
}

func TestVideoChunkRoundTrip(t *testing.T) {
	payload := []byte{0, 0, 0, 1, 0x65}
	raw := encodeVideoChunk(123456, 33333, true, payload)

	got, ok := decodeVideoChunk(raw)
	if !ok {
		t.Fatal("decodeVideoChunk returned false")
	}
	if !got.KeyFrame || got.Timestamp != 123456 || got.Duration != 33333 {
		t.Fatalf("chunk = %#v", got)
	}
	if !bytes.Equal(got.Data, payload) {
		t.Fatalf("data = %v, want %v", got.Data, payload)
	}
}
