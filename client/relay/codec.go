package relay

import (
	"fmt"
	"strconv"
	"strings"
)

// VideoFormat mirrors moonlight-common-c's VIDEO_FORMAT_* bit layout so future
// encoder negotiation can reason about codec family, bit depth, and chroma mode.
type VideoFormat uint16

const (
	VideoFormatH264          VideoFormat = 0x0001
	VideoFormatH264High8444  VideoFormat = 0x0004
	VideoFormatH265          VideoFormat = 0x0100
	VideoFormatH265Main10    VideoFormat = 0x0200
	VideoFormatH265Rext8444  VideoFormat = 0x0400
	VideoFormatH265Rext10444 VideoFormat = 0x0800
	VideoFormatAV1Main8      VideoFormat = 0x1000
	VideoFormatAV1Main10     VideoFormat = 0x2000
	VideoFormatAV1High8444   VideoFormat = 0x4000
	VideoFormatAV1High10444  VideoFormat = 0x8000
	VideoFormatMaskH264      VideoFormat = 0x000F
	VideoFormatMaskH265      VideoFormat = 0x0F00
	VideoFormatMaskAV1       VideoFormat = 0xF000
	VideoFormatMask10Bit     VideoFormat = 0xAA00
	VideoFormatMaskYUV444    VideoFormat = 0xCC04
)

type codecFamily string

const (
	codecFamilyH264 codecFamily = "h264"
	codecFamilyH265 codecFamily = "h265"
	codecFamilyAV1  codecFamily = "av1"
)

func (f VideoFormat) Family() (codecFamily, bool) {
	switch {
	case f&VideoFormatMaskH264 != 0:
		return codecFamilyH264, true
	case f&VideoFormatMaskH265 != 0:
		return codecFamilyH265, true
	case f&VideoFormatMaskAV1 != 0:
		return codecFamilyAV1, true
	default:
		return "", false
	}
}

func (f VideoFormat) Is10Bit() bool {
	return f&VideoFormatMask10Bit != 0
}

func (f VideoFormat) IsYUV444() bool {
	return f&VideoFormatMaskYUV444 != 0
}

func videoFormatFromCodecString(codec string) (VideoFormat, bool) {
	codec = strings.ToLower(strings.TrimSpace(codec))
	switch {
	case strings.HasPrefix(codec, "avc1.") || strings.HasPrefix(codec, "avc3."):
		return VideoFormatH264, true
	case strings.HasPrefix(codec, "hvc1.") || strings.HasPrefix(codec, "hev1."):
		return hevcFormatFromCodecString(codec)
	case strings.HasPrefix(codec, "av01."):
		return av1FormatFromCodecString(codec)
	default:
		return 0, false
	}
}

func validateVideoCodecString(codec string) error {
	if _, ok := videoFormatFromCodecString(codec); !ok {
		return fmt.Errorf("unsupported video codec %q; expected avc1/avc3, hvc1/hev1, or av01 WebCodecs string", codec)
	}
	return nil
}

func hevcFormatFromCodecString(codec string) (VideoFormat, bool) {
	parts := strings.Split(codec, ".")
	if len(parts) < 2 {
		return VideoFormatH265, true
	}
	switch parts[1] {
	case "2":
		return VideoFormatH265Main10, true
	case "4":
		return VideoFormatH265Rext8444, true
	default:
		return VideoFormatH265, true
	}
}

func av1FormatFromCodecString(codec string) (VideoFormat, bool) {
	parts := strings.Split(codec, ".")
	if len(parts) < 4 {
		return VideoFormatAV1Main8, true
	}

	profile, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, false
	}
	bitDepth, err := strconv.Atoi(parts[3])
	if err != nil {
		return 0, false
	}

	switch {
	case profile == 1 && bitDepth >= 10:
		return VideoFormatAV1High10444, true
	case profile == 1:
		return VideoFormatAV1High8444, true
	case bitDepth >= 10:
		return VideoFormatAV1Main10, true
	default:
		return VideoFormatAV1Main8, true
	}
}
