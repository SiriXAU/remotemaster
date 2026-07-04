package relay

import (
	"bytes"
	"fmt"
	"image"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/chai2010/webp"
)

// wireVideoEncoder converts captured frames into one or more binary protocol
// messages. Sunshine's host pipeline maps to this boundary: encode a frame,
// keep the encoded bytes and IDR metadata, then packetize for transport.
type wireVideoEncoder interface {
	Encode(image.Image) ([][]byte, error)
	Close() error
}

type webpVideoEncoder struct {
	w, h    int
	quality float32
}

func newWireVideoEncoder(w, h, fps int, quality float32) wireVideoEncoder {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("REMOTEMASTER_VIDEO_CODEC")))
	switch mode {
	case "", "auto", "h264", "ffmpeg-h264":
		encoder, err := newFFmpegH264Encoder(w, h, fps)
		if err == nil {
			log.Printf("video encoder: h264 via ffmpeg")
			return encoder
		}
		log.Printf("video encoder: h264 unavailable (%v); falling back to webp", err)
	case "webp":
	default:
		log.Printf("video encoder: unsupported REMOTEMASTER_VIDEO_CODEC=%q; falling back to webp", os.Getenv("REMOTEMASTER_VIDEO_CODEC"))
	}
	return newWebPVideoEncoder(w, h, quality)
}

func newWebPVideoEncoder(w, h int, quality float32) wireVideoEncoder {
	log.Printf("video encoder: webp")
	return &webpVideoEncoder{w: w, h: h, quality: quality}
}

func (e *webpVideoEncoder) Encode(img image.Image) ([][]byte, error) {
	data, err := webp.EncodeRGBA(img, e.quality)
	if err != nil {
		return nil, fmt.Errorf("webp encode: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("webp encode: empty frame")
	}
	return [][]byte{encodeFrame(e.w, e.h, data)}, nil
}

func (e *webpVideoEncoder) Close() error {
	return nil
}

type h264AccessUnit struct {
	data     []byte
	keyFrame bool
}

type ffmpegH264Encoder struct {
	w, h      int
	fps       int
	codec     string
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	packetCh  chan h264AccessUnit
	errCh     chan error
	sentCfg   bool
	timestamp uint64
	duration  uint32
}

func newFFmpegH264Encoder(w, h, fps int) (*ffmpegH264Encoder, error) {
	ffmpegPath := strings.TrimSpace(os.Getenv("REMOTEMASTER_FFMPEG"))
	if ffmpegPath == "" {
		var err error
		ffmpegPath, err = findFFmpeg()
		if err != nil {
			return nil, fmt.Errorf("ffmpeg not found; set REMOTEMASTER_FFMPEG or install ffmpeg")
		}
	}

	encoderName := strings.TrimSpace(os.Getenv("REMOTEMASTER_H264_ENCODER"))
	if encoderName == "" {
		encoderName = defaultH264Encoder()
	}
	codec := strings.TrimSpace(os.Getenv("REMOTEMASTER_VIDEO_CODEC_STRING"))
	if codec == "" {
		codec = "avc1.42E01F"
	}
	if err := validateVideoCodecString(codec); err != nil {
		return nil, err
	}
	bitrateKbps := envClampedInt(
		"REMOTEMASTER_VIDEO_BITRATE_KBPS",
		defaultVideoBitrateKbps(w, h, fps),
		500,
		100000,
	)

	args := ffmpegH264Args(w, h, fps, encoderName, bitrateKbps)
	cmd := exec.Command(ffmpegPath, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("ffmpeg stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("ffmpeg stderr: %w", err)
	}

	enc := &ffmpegH264Encoder{
		w:        w,
		h:        h,
		fps:      fps,
		codec:    codec,
		cmd:      cmd,
		stdin:    stdin,
		packetCh: make(chan h264AccessUnit, fps*2),
		errCh:    make(chan error, 1),
		duration: uint32(1000000 / maxInt(fps, 1)),
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}
	go enc.readPackets(stdout)
	go enc.readErrors(stderr)
	return enc, nil
}

func defaultH264Encoder() string {
	if runtime.GOOS == "windows" {
		return "h264_mf"
	}
	return "libx264"
}

func findFFmpeg() (string, error) {
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "ffmpeg.exe")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return exec.LookPath("ffmpeg")
}

func defaultVideoBitrateKbps(w, h, fps int) int {
	// High-quality remote desktop baseline: about 0.18 bits/pixel/frame, with a
	// floor that keeps 720p/1080p text and UI detail crisp on fast links.
	kbps := w * h * maxInt(fps, 1) * 18 / 100000
	return maxInt(kbps, 6000)
}

func ffmpegH264Args(w, h, fps int, encoderName string, bitrateKbps int) []string {
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", w, h),
		"-r", strconv.Itoa(fps),
		"-i", "pipe:0",
		"-an",
		"-c:v", encoderName,
		"-bf", "0",
		"-g", strconv.Itoa(maxInt(fps*2, 1)),
		"-b:v", fmt.Sprintf("%dk", bitrateKbps),
		"-maxrate", fmt.Sprintf("%dk", bitrateKbps),
		"-bufsize", fmt.Sprintf("%dk", bitrateKbps),
	}

	if encoderName == "libx264" {
		args = append(args,
			"-preset", "veryfast",
			"-tune", "zerolatency",
			"-profile:v", "baseline",
			"-pix_fmt", "yuv420p",
			"-x264-params", "scenecut=0:repeat-headers=1:aud=1",
		)
	}

	args = append(args,
		"-bsf:v", "h264_metadata=aud=insert",
		"-f", "h264",
		"pipe:1",
	)
	return args
}

func (e *ffmpegH264Encoder) Encode(img image.Image) ([][]byte, error) {
	nrgba, ok := img.(*image.NRGBA)
	if !ok {
		return nil, fmt.Errorf("h264 encode: unexpected image type %T", img)
	}

	messages := make([][]byte, 0, 2)
	if !e.sentCfg {
		cfg, err := encodeVideoConfig(e.w, e.h, e.codec, nil)
		if err != nil {
			return nil, err
		}
		messages = append(messages, cfg)
		e.sentCfg = true
	}

	if err := e.writeFrame(nrgba); err != nil {
		return nil, err
	}

	select {
	case packet, ok := <-e.packetCh:
		if !ok {
			return nil, fmt.Errorf("h264 encoder stopped")
		}
		messages = append(messages, e.encodePacket(packet))
	case err := <-e.errCh:
		return nil, err
	case <-time.After(750 * time.Millisecond):
		return messages, nil
	}

	for {
		select {
		case packet, ok := <-e.packetCh:
			if !ok {
				return messages, nil
			}
			messages = append(messages, e.encodePacket(packet))
		default:
			return messages, nil
		}
	}
}

func (e *ffmpegH264Encoder) Close() error {
	if e.stdin != nil {
		_ = e.stdin.Close()
	}
	if e.cmd != nil && e.cmd.Process != nil {
		done := make(chan error, 1)
		go func() { done <- e.cmd.Wait() }()
		select {
		case err := <-done:
			return err
		case <-time.After(time.Second):
			_ = e.cmd.Process.Kill()
			return <-done
		}
	}
	return nil
}

func (e *ffmpegH264Encoder) writeFrame(img *image.NRGBA) error {
	rowLen := e.w * 4
	if img.Rect.Dx() != e.w || img.Rect.Dy() != e.h {
		return fmt.Errorf("h264 encode: frame size %dx%d does not match encoder %dx%d", img.Rect.Dx(), img.Rect.Dy(), e.w, e.h)
	}
	if img.Stride == rowLen {
		_, err := e.stdin.Write(img.Pix[:rowLen*e.h])
		return err
	}
	for y := 0; y < e.h; y++ {
		start := y * img.Stride
		if _, err := e.stdin.Write(img.Pix[start : start+rowLen]); err != nil {
			return err
		}
	}
	return nil
}

func (e *ffmpegH264Encoder) encodePacket(packet h264AccessUnit) []byte {
	msg := encodeVideoChunk(e.timestamp, e.duration, packet.keyFrame, packet.data)
	e.timestamp += uint64(e.duration)
	return msg
}

func (e *ffmpegH264Encoder) readPackets(r io.Reader) {
	if err := parseAnnexBAccessUnits(r, e.packetCh); err != nil {
		select {
		case e.errCh <- err:
		default:
		}
	}
}

func (e *ffmpegH264Encoder) readErrors(r io.Reader) {
	var b bytes.Buffer
	_, _ = io.Copy(&b, io.LimitReader(r, 64*1024))
	if msg := strings.TrimSpace(b.String()); msg != "" {
		select {
		case e.errCh <- fmt.Errorf("ffmpeg: %s", msg):
		default:
		}
	}
}

func parseAnnexBAccessUnits(r io.Reader, out chan<- h264AccessUnit) error {
	defer close(out)

	parser := h264AnnexBParser{out: out}
	buf := make([]byte, 0, 64*1024)
	tmp := make([]byte, 32*1024)

	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			buf = parser.consumeCompleteNALs(buf)
		}
		if err != nil {
			if err == io.EOF {
				parser.consumeRemainder(buf)
				parser.flush()
				return nil
			}
			return fmt.Errorf("read ffmpeg h264: %w", err)
		}
	}
}

type h264AnnexBParser struct {
	out    chan<- h264AccessUnit
	au     []byte
	hasVCL bool
}

func (p *h264AnnexBParser) consumeCompleteNALs(buf []byte) []byte {
	for {
		first, firstLen := findAnnexBStartCode(buf, 0)
		if first < 0 {
			return keepAnnexBTail(buf)
		}
		if first > 0 {
			buf = buf[first:]
			first = 0
		}

		next, _ := findAnnexBStartCode(buf, first+firstLen)
		if next < 0 {
			return buf
		}
		p.consumeNAL(buf[:next])
		buf = buf[next:]
	}
}

func (p *h264AnnexBParser) consumeRemainder(buf []byte) {
	if idx, _ := findAnnexBStartCode(buf, 0); idx >= 0 {
		p.consumeNAL(buf[idx:])
	}
}

func (p *h264AnnexBParser) consumeNAL(nal []byte) {
	startLen := annexBStartCodeLen(nal)
	if startLen == 0 || len(nal) <= startLen {
		return
	}
	nalType := nal[startLen] & 0x1f
	if nalType == 9 && p.hasVCL {
		p.flush()
	}
	p.au = append(p.au, nal...)
	if nalType == 1 || nalType == 5 {
		p.hasVCL = true
	}
}

func (p *h264AnnexBParser) flush() {
	if len(p.au) == 0 {
		return
	}
	if !p.hasVCL {
		p.au = p.au[:0]
		return
	}
	data := append([]byte(nil), p.au...)
	p.out <- h264AccessUnit{data: data, keyFrame: h264AccessUnitHasIDR(data)}
	p.au = p.au[:0]
	p.hasVCL = false
}

func h264AccessUnitHasIDR(data []byte) bool {
	for offset := 0; ; {
		idx, startLen := findAnnexBStartCode(data, offset)
		if idx < 0 {
			return false
		}
		nalStart := idx + startLen
		if nalStart < len(data) && data[nalStart]&0x1f == 5 {
			return true
		}
		offset = nalStart + 1
	}
}

func findAnnexBStartCode(data []byte, offset int) (int, int) {
	for i := maxInt(offset, 0); i+3 <= len(data); i++ {
		if i+4 <= len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
			return i, 4
		}
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			return i, 3
		}
	}
	return -1, 0
}

func annexBStartCodeLen(data []byte) int {
	if len(data) >= 4 && data[0] == 0 && data[1] == 0 && data[2] == 0 && data[3] == 1 {
		return 4
	}
	if len(data) >= 3 && data[0] == 0 && data[1] == 0 && data[2] == 1 {
		return 3
	}
	return 0
}

func keepAnnexBTail(data []byte) []byte {
	if len(data) <= 3 {
		return data
	}
	return data[len(data)-3:]
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
