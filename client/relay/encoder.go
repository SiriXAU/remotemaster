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

// packetWaitTimeout bounds how long Encode waits for ffmpeg to emit an
// encoded packet for the frame just written before returning early.
const packetWaitTimeout = 750 * time.Millisecond

type webpVideoEncoder struct {
	w, h    int
	quality float32
}

func newWireVideoEncoder(w, h, fps int, quality float32) wireVideoEncoder {
	rawMode := os.Getenv("REMOTEMASTER_VIDEO_CODEC")
	mode := strings.ToLower(strings.TrimSpace(rawMode))
	explicitH264 := mode == "h264" || mode == "ffmpeg-h264"
	switch mode {
	case "", "auto", "h264", "ffmpeg-h264":
		encoder, err := newFFmpegH264Encoder(w, h, fps)
		if err == nil {
			log.Printf("video encoder: h264 via ffmpeg")
			return encoder
		}
		if explicitH264 {
			log.Printf("video encoder: REMOTEMASTER_VIDEO_CODEC=%q was explicitly requested but ffmpeg h264 setup failed (%v); falling back to webp", rawMode, err)
		} else {
			log.Printf("video encoder: h264 unavailable (%v); falling back to webp", err)
		}
		// The fallback reason was already logged above with full context —
		// avoid a second, generic "video encoder: webp" log for the same
		// event.
		return &webpVideoEncoder{w: w, h: h, quality: quality}
	case "webp":
	default:
		log.Printf("video encoder: unsupported REMOTEMASTER_VIDEO_CODEC=%q; falling back to webp", rawMode)
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
	w, h        int
	codec       string
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	packetCh    chan h264AccessUnit
	errCh       chan error
	sentCfg     bool
	timestamp   uint64
	duration    uint32
	packetTimer *time.Timer
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
	// Codec-string validation happens once, at the wire-protocol boundary in
	// encodeVideoConfig (proto.go), which the first Encode call will
	// exercise before anything is written to the connection.
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
		codec:    codec,
		cmd:      cmd,
		stdin:    stdin,
		packetCh: make(chan h264AccessUnit, fps*2),
		errCh:    make(chan error, 1),
		duration: uint32(1000000 / max(fps, 1)),
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
	kbps := w * h * max(fps, 1) * 18 / 100000
	return max(kbps, 6000)
}

func ffmpegH264Args(w, h, fps int, encoderName string, bitrateKbps int) []string {
	bitrate := fmt.Sprintf("%dk", bitrateKbps)
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
		"-g", strconv.Itoa(max(fps*2, 1)),
		"-b:v", bitrate,
		"-maxrate", bitrate,
		"-bufsize", bitrate,
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
		// dump_extra=freq=keyframe injects extradata (SPS/PPS) before every
		// keyframe regardless of encoder. libx264 already repeats headers via
		// x264-params above, but the default Windows encoder (h264_mf) does
		// not, so without this a mid-stream keyframe recovery (the 0x08
		// config message carries no description) can fail to decode.
		"-bsf:v", "h264_metadata=aud=insert,dump_extra=freq=keyframe",
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

	if e.packetTimer == nil {
		e.packetTimer = time.NewTimer(packetWaitTimeout)
	} else {
		// Reuse the timer instead of allocating a new one per frame. Per the
		// time.Timer docs, Reset must only be called on a stopped or expired
		// timer with its channel drained.
		if !e.packetTimer.Stop() {
			select {
			case <-e.packetTimer.C:
			default:
			}
		}
		e.packetTimer.Reset(packetWaitTimeout)
	}

	select {
	case packet, ok := <-e.packetCh:
		if !ok {
			return nil, fmt.Errorf("h264 encoder stopped")
		}
		messages = append(messages, e.encodePacket(packet))
	case err := <-e.errCh:
		return nil, err
	case <-e.packetTimer.C:
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
	if e.packetTimer != nil {
		e.packetTimer.Stop()
	}
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
	// The capture above only keeps the first 64KiB for the error message.
	// If ffmpeg keeps writing to stderr beyond that, its pipe must still be
	// drained or the process blocks writing to a full pipe, hanging the
	// stream. Discard everything after the captured prefix.
	_, _ = io.Copy(io.Discard, r)
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
	out      chan<- h264AccessUnit
	au       []byte
	hasVCL   bool
	auHasIDR bool

	// scanFrom is how far into buf the search for the next start code has
	// already progressed with no match, so a subsequent call (e.g. after
	// more bytes arrive from a large in-flight NAL) can resume the scan
	// instead of rescanning already-checked bytes from offset 0. Without
	// this, accumulating one large NAL across many small reads is O(N^2) in
	// the NAL size.
	scanFrom int
}

func (p *h264AnnexBParser) consumeCompleteNALs(buf []byte) []byte {
	for {
		first, firstLen := findAnnexBStartCode(buf, 0)
		if first < 0 {
			p.scanFrom = 0
			return keepAnnexBTail(buf)
		}
		if first > 0 {
			buf = buf[first:]
			first = 0
			p.scanFrom = 0
		}

		from := first + firstLen
		if p.scanFrom > from {
			from = p.scanFrom
		}
		next, _ := findAnnexBStartCode(buf, from)
		if next < 0 {
			// Remember how far we've scanned. Back up by up to 3 bytes so a
			// start code that straddles this read's boundary (only
			// partially visible so far) is still found once more bytes
			// arrive.
			p.scanFrom = max(len(buf)-3, from)
			return buf
		}
		p.consumeNAL(buf[:next])
		buf = buf[next:]
		p.scanFrom = 0
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
	if nalType == 5 {
		p.auHasIDR = true
	}
}

func (p *h264AnnexBParser) flush() {
	if len(p.au) == 0 {
		return
	}
	if !p.hasVCL {
		p.au = p.au[:0]
		p.auHasIDR = false
		return
	}
	// The copy below is required: p.au's backing array is reused for the
	// next access unit right after this send, but out is a channel to
	// another goroutine, so the sent data must be independently owned.
	data := append([]byte(nil), p.au...)
	p.out <- h264AccessUnit{data: data, keyFrame: p.auHasIDR}
	p.au = p.au[:0]
	p.hasVCL = false
	p.auHasIDR = false
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
	for i := max(offset, 0); i+3 <= len(data); i++ {
		if i+4 <= len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
			return i, 4
		}
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			return i, 3
		}
	}
	return -1, 0
}

// annexBStartCodeLen reports the length of the start code data begins with
// (0, 3, or 4), reusing findAnnexBStartCode's byte-comparison logic instead
// of duplicating it.
func annexBStartCodeLen(data []byte) int {
	idx, length := findAnnexBStartCode(data, 0)
	if idx != 0 {
		return 0
	}
	return length
}

func keepAnnexBTail(data []byte) []byte {
	if len(data) <= 3 {
		return data
	}
	return data[len(data)-3:]
}
