package relay

import (
	"bytes"
	"image"
	"image/color"
	"os"
	"testing"
)

func TestParseAnnexBAccessUnitsGroupsOnAUD(t *testing.T) {
	stream := bytes.Join([][]byte{
		annexBNAL(9, 0x10),       // AUD
		annexBNAL(7, 0x64),       // SPS
		annexBNAL(8, 0x1f),       // PPS
		annexBNAL(5, 0x88, 0x84), // IDR slice
		annexBNAL(9, 0x10),       // next AUD starts a new access unit
		annexBNAL(1, 0x9a, 0x22), // non-IDR slice
		annexBNAL(9, 0x10),       // trailing AUD without a slice is ignored
	}, nil)

	out := make(chan h264AccessUnit, 4)
	if err := parseAnnexBAccessUnits(bytes.NewReader(stream), out); err != nil {
		t.Fatalf("parseAnnexBAccessUnits: %v", err)
	}

	var got []h264AccessUnit
	for packet := range out {
		got = append(got, packet)
	}
	if len(got) != 2 {
		t.Fatalf("access units = %d, want 2", len(got))
	}
	if !got[0].keyFrame {
		t.Fatal("first access unit was not marked as a key frame")
	}
	if got[1].keyFrame {
		t.Fatal("second access unit was incorrectly marked as a key frame")
	}
	if !bytes.Contains(got[0].data, annexBNAL(7, 0x64)) {
		t.Fatal("first access unit does not contain SPS")
	}
}

func TestH264AccessUnitHasIDR(t *testing.T) {
	if !h264AccessUnitHasIDR(bytes.Join([][]byte{annexBNAL(9, 0x10), annexBNAL(5, 0x88)}, nil)) {
		t.Fatal("IDR access unit was not detected")
	}
	if h264AccessUnitHasIDR(bytes.Join([][]byte{annexBNAL(9, 0x10), annexBNAL(1, 0x88)}, nil)) {
		t.Fatal("non-IDR access unit was detected as IDR")
	}
}

func TestNewWireVideoEncoderAutoFallsBackToWebP(t *testing.T) {
	t.Setenv("REMOTEMASTER_VIDEO_CODEC", "auto")
	t.Setenv("REMOTEMASTER_FFMPEG", "/definitely/missing/ffmpeg")

	encoder := newWireVideoEncoder(64, 64, 10, 65)
	defer encoder.Close()
	if _, ok := encoder.(*webpVideoEncoder); !ok {
		t.Fatalf("encoder = %T, want *webpVideoEncoder", encoder)
	}
}

func TestFFmpegH264EncoderSmoke(t *testing.T) {
	ffmpegPath := os.Getenv("REMOTEMASTER_FFMPEG_TEST")
	if ffmpegPath == "" {
		t.Skip("set REMOTEMASTER_FFMPEG_TEST to run FFmpeg H.264 smoke test")
	}
	t.Setenv("REMOTEMASTER_FFMPEG", ffmpegPath)
	t.Setenv("REMOTEMASTER_H264_ENCODER", "libx264")
	t.Setenv("REMOTEMASTER_VIDEO_CODEC_STRING", "avc1.42E01F")

	encoder, err := newFFmpegH264Encoder(64, 64, 10)
	if err != nil {
		t.Fatalf("newFFmpegH264Encoder: %v", err)
	}
	defer encoder.Close()

	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	var sawConfig, sawChunk bool
	for frame := 0; frame < 8 && !sawChunk; frame++ {
		fillTestFrame(img, frame)
		messages, err := encoder.Encode(img)
		if err != nil {
			t.Fatalf("Encode frame %d: %v", frame, err)
		}
		for _, msg := range messages {
			if len(msg) == 0 {
				continue
			}
			if msg[0] == binVideoConfig {
				sawConfig = true
			}
			if msg[0] == binVideoChunk {
				chunk, ok := decodeVideoChunk(msg)
				if !ok {
					t.Fatalf("decodeVideoChunk returned false")
				}
				if len(chunk.Data) == 0 {
					t.Fatal("video chunk has empty payload")
				}
				sawChunk = true
			}
		}
	}
	if !sawConfig {
		t.Fatal("encoder did not emit a video config")
	}
	if !sawChunk {
		t.Fatal("encoder did not emit a video chunk")
	}
}

func fillTestFrame(img *image.NRGBA, frame int) {
	for y := 0; y < img.Rect.Dy(); y++ {
		for x := 0; x < img.Rect.Dx(); x++ {
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(x*4 + frame*9),
				G: uint8(y*4 + frame*5),
				B: uint8((x+y)*2 + frame*3),
				A: 255,
			})
		}
	}
}

func annexBNAL(nalType byte, payload ...byte) []byte {
	nal := []byte{0, 0, 0, 1, nalType}
	return append(nal, payload...)
}
