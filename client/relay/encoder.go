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
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/chai2010/webp"
)

// wireVideoEncoder converts captured frames into one or more binary protocol
// messages. Sunshine's host pipeline maps to this boundary: encode a frame,
// keep the encoded bytes and IDR metadata, then packetize for transport.
type wireVideoEncoder interface {
	Encode(image.Image) ([][]byte, error)
	Close() error
}

// packetWait bounds how long Encode waits for ffmpeg to emit an encoded
// packet for the frame just written before returning early. Keeping it to
// roughly one frame interval pipelines capture and encoding: a slow encoder
// (e.g. software H.264 inside a VM) delays its output to later Encode calls
// instead of stalling the capture loop on every frame.
func packetWait(fps int) time.Duration {
	d := time.Second / time.Duration(max(fps, 1))
	if d < 33*time.Millisecond {
		d = 33 * time.Millisecond
	}
	if d > 250*time.Millisecond {
		d = 250 * time.Millisecond
	}
	return d
}

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

func newWireVideoEncoder(w, h, fps int, quality float32) wireVideoEncoder {
	rawMode := os.Getenv("REMOTEMASTER_VIDEO_CODEC")
	mode := strings.ToLower(strings.TrimSpace(rawMode))
	switch mode {
	case "h264", "ffmpeg-h264":
		encoder, err := newFFmpegH264Encoder(w, h, fps)
		if err == nil {
			log.Printf("video encoder: h264 via ffmpeg")
			return encoder
		}
		log.Printf("video encoder: REMOTEMASTER_VIDEO_CODEC=%q was explicitly requested but ffmpeg h264 setup failed (%v); falling back to webp", rawMode, err)
	case "", "auto", "webp":
		// Dirty-region WebP is the default: for desktop content it beats
		// H.264 on latency and text sharpness, and adaptive quality keeps
		// the frame rate up during full-screen motion. H.264 remains the
		// bandwidth-efficient opt-in for slow links (REMOTEMASTER_VIDEO_CODEC=h264).
	default:
		log.Printf("video encoder: unsupported REMOTEMASTER_VIDEO_CODEC=%q; falling back to webp", rawMode)
	}
	return newWebPVideoEncoder(w, h, fps, quality)
}

func newWebPVideoEncoder(w, h, fps int, quality float32) wireVideoEncoder {
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

// DrainPackets runs on ticks where the screen didn't change. That idle
// moment is the cheap place to send the periodic full refresh: the encode
// cost stutters nothing because no motion is on screen, and it repaints at
// the quality cap (idle encodes have long since restored q) so the desktop
// sharpens right after activity stops.
func (e *webpVideoEncoder) DrainPackets() [][]byte {
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
	wait        time.Duration
	packetTimer *time.Timer

	// framesIn/packetsOut track pipeline depth: how many frames are inside
	// ffmpeg (and the AU parser) awaiting output. Sampled by the capture
	// loop's stats logging to attribute perceived latency.
	framesIn   atomic.Int64
	packetsOut atomic.Int64
}

// PipelineDepth reports how many written frames have not yet produced an
// encoded access unit.
func (e *ffmpegH264Encoder) PipelineDepth() int64 {
	return e.framesIn.Load() - e.packetsOut.Load()
}

// DrainPackets returns any encoded access units that are already waiting,
// without blocking. The capture loop calls this on ticks where the frame was
// skipped (unchanged screen), so output from a slow or pipelined encoder is
// still delivered promptly instead of sitting until the next screen change.
func (e *ffmpegH264Encoder) DrainPackets() [][]byte {
	var messages [][]byte
	for {
		select {
		case packet, ok := <-e.packetCh:
			if !ok {
				return messages
			}
			messages = append(messages, e.encodePacket(packet))
		default:
			return messages
		}
	}
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
		wait:     packetWait(fps),
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
	}

	// H.264 4:2:0 needs even dimensions; encoders like h264_mf hard-reject
	// odd sizes (MF_E_INVALIDMEDIATYPE). Screens can be odd-sized — VMs and
	// scaled displays especially — so shave at most one pixel per axis.
	if cw, ch := w&^1, h&^1; cw != w || ch != h {
		args = append(args, "-vf", fmt.Sprintf("crop=%d:%d:0:0", cw, ch))
	}

	args = append(args,
		"-c:v", encoderName,
		"-bf", "0",
		"-g", strconv.Itoa(max(fps*2, 1)),
		"-b:v", bitrate,
		"-maxrate", bitrate,
		"-bufsize", bitrate,
	)

	if encoderName == "libx264" {
		args = append(args,
			"-preset", "veryfast",
			"-tune", "zerolatency",
			"-profile:v", "baseline",
			"-pix_fmt", "yuv420p",
			"-x264-params", "scenecut=0:repeat-headers=1:aud=1",
		)
	}
	if strings.HasSuffix(encoderName, "_mf") {
		// Media Foundation buffers several frames by default; the
		// display_remoting scenario is MF's low-latency mode for exactly
		// this workload. Note: the MF encoders register no named -profile
		// constants (passing one aborts ffmpeg at startup) and already
		// default to H.264 Base profile, which signals no-reorder to
		// decoders like Safari's VideoToolbox.
		args = append(args, "-scenario", "display_remoting")
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
		// Advertise the encoder's coded size: odd screen dimensions are
		// cropped down to even by the ffmpeg filter graph (see
		// ffmpegH264Args), so the stream is up to one pixel smaller than
		// the capture on each axis.
		cfg, err := encodeVideoConfig(e.w&^1, e.h&^1, e.codec, nil)
		if err != nil {
			return nil, err
		}
		messages = append(messages, cfg)
		e.sentCfg = true
	}

	if err := e.writeFrame(nrgba); err != nil {
		return nil, err
	}
	e.framesIn.Add(1)

	if e.packetTimer == nil {
		e.packetTimer = time.NewTimer(e.wait)
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
		e.packetTimer.Reset(e.wait)
	}

	select {
	case packet, ok := <-e.packetCh:
		if !ok {
			// The parser goroutine closed the channel: ffmpeg exited. Its
			// stderr is captured on a separate goroutine that may still be
			// reading, so give it a moment rather than losing the reason.
			select {
			case err := <-e.errCh:
				return nil, fmt.Errorf("h264 encoder stopped: %w", err)
			case <-time.After(time.Second):
				return nil, fmt.Errorf("h264 encoder stopped")
			}
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
	e.packetsOut.Add(1)
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
