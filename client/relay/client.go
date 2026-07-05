package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"image"
	"log"
	"sync"
	"time"

	"nhooyr.io/websocket"

	"github.com/sirixau/remotemaster/client/capture"
	"github.com/sirixau/remotemaster/client/clipboard"
	"github.com/sirixau/remotemaster/client/input"
)

// dialErr wraps errors that occur before a session code is received,
// so callers can distinguish "never connected" from "disconnected".
type dialErr struct{ err error }

func (e dialErr) Error() string { return "dial: " + e.err.Error() }
func (e dialErr) Unwrap() error { return e.err }

const (
	defaultFrameQuality = 65
	defaultTargetFPS    = 15
	dialTimeout         = 10 * time.Second
)

// ctrlMsg is the JSON control message struct used during session setup.
type ctrlMsg struct {
	Type string `json:"type"`
	Code string `json:"code,omitempty"`
	Msg  string `json:"msg,omitempty"`
}

// Client manages the WebSocket relay connection and drives the capture loop.
type Client struct {
	serverURL string
	cap       capture.Capturer
	inj       input.Injector
	onCode    func(code string)
	onConnect func()
	onDisconn func()
	// OnConnFail is called when the server cannot be reached at all (dial error),
	// as opposed to onDisconn which fires after a working session drops.
	OnConnFail func()

	// Clip, when set, enables bidirectional text clipboard sync with the agent.
	Clip clipboard.Clipboard

	targetFPS    int
	frameQuality float32

	clipMu     sync.Mutex
	clipPrimed bool
	lastClip   string
}

func New(serverURL string, cap capture.Capturer, inj input.Injector,
	onCode func(string), onConnect func(), onDisconn func()) *Client {
	return &Client{
		serverURL: serverURL,
		cap:       cap,
		inj:       inj,
		onCode:    onCode,
		onConnect: onConnect,
		onDisconn: onDisconn,
		// Overridable per machine without a rebuild — useful on slow links
		// (lower both) or fast LANs (raise FPS).
		targetFPS:    envClampedInt("REMOTEMASTER_FPS", defaultTargetFPS, 1, 60),
		frameQuality: float32(envClampedInt("REMOTEMASTER_QUALITY", defaultFrameQuality, 1, 100)),
	}
}

// Run connects to the relay server with automatic exponential back-off.
// Blocks until ctx is cancelled.
func (c *Client) Run(ctx context.Context) {
	delay := time.Second
	for {
		if err := c.connect(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			var de dialErr
			if errors.As(err, &de) {
				log.Printf("relay: cannot reach server at %s (%v) — retrying in %s (check scheme: ws vs wss)", c.serverURL, de.err, delay)
				if c.OnConnFail != nil {
					c.OnConnFail()
				}
			} else {
				log.Printf("relay: disconnected (%v), retrying in %s", err, delay)
				if c.onDisconn != nil {
					c.onDisconn()
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			if delay < 16*time.Second {
				delay *= 2
			}
		} else {
			delay = time.Second
		}
	}
}

func (c *Client) connect(ctx context.Context) error {
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	conn, _, err := websocket.Dial(dialCtx, c.serverURL+"/ws/client", nil)
	if err != nil {
		return dialErr{fmt.Errorf("connect to %s: %w", c.serverURL, err)}
	}
	defer conn.CloseNow()

	// Wait for the "registered" message to get our session code.
	var reg ctrlMsg
	if err := readJSON(dialCtx, conn, &reg); err != nil {
		return dialErr{fmt.Errorf("waiting for registration: %w", err)}
	}
	if reg.Type != "registered" {
		return dialErr{fmt.Errorf("expected 'registered', got %q", reg.Type)}
	}
	if c.onCode != nil {
		c.onCode(reg.Code)
	}
	log.Printf("relay: session code = %s", reg.Code)

	// Wait for "agent_connected" before starting the capture loop.
	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	agentCh := make(chan struct{}, 1)
	inputCh := make(chan input.Event, 64)
	readErrCh := make(chan error, 1)

	go c.readPump(connCtx, conn, agentCh, inputCh, readErrCh)
	go c.injectLoop(connCtx, inputCh)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-agentCh:
	case err := <-readErrCh:
		if err == nil {
			return fmt.Errorf("connection closed while waiting for agent")
		}
		return fmt.Errorf("read while waiting for agent: %w", err)
	}

	if c.onConnect != nil {
		c.onConnect()
	}

	captureErrCh := make(chan error, 1)
	go func() {
		captureErrCh <- c.captureLoop(connCtx, conn)
	}()
	go c.clipboardLoop(connCtx, conn)

	select {
	case <-ctx.Done():
		connCancel()
		return ctx.Err()
	case err := <-readErrCh:
		connCancel()
		if err == nil {
			return fmt.Errorf("connection closed")
		}
		return fmt.Errorf("read: %w", err)
	case err := <-captureErrCh:
		connCancel()
		return err
	}
}

// readPump handles both JSON control messages and binary input events.
func (c *Client) readPump(ctx context.Context, conn *websocket.Conn, agentCh chan<- struct{}, inputCh chan<- input.Event, errCh chan<- error) {
	report := func(err error) {
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		select {
		case errCh <- err:
		default:
		}
	}

	for {
		mt, b, err := conn.Read(ctx)
		if err != nil {
			report(err)
			return
		}
		if mt == websocket.MessageBinary {
			if len(b) > 0 && b[0] == binClipboard {
				if text, ok := decodeClipboard(b); ok {
					c.applyRemoteClipboard(text)
				}
				continue
			}
			ev, ok := decodeEvent(b)
			if !ok {
				continue
			}
			select {
			case inputCh <- ev:
			default:
			}
			continue
		}
		// JSON control message
		var m ctrlMsg
		if err := json.Unmarshal(b, &m); err != nil {
			continue
		}
		switch m.Type {
		case "agent_connected":
			select {
			case agentCh <- struct{}{}:
			default:
			}
		case "agent_disconnected", "disconnect":
			if c.onDisconn != nil {
				c.onDisconn()
			}
			report(fmt.Errorf("agent disconnected"))
			return
		}
	}
}

func (c *Client) injectLoop(ctx context.Context, ch chan input.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			if c.inj == nil {
				continue
			}
			if err := c.inj.Inject(ev); err != nil {
				log.Printf("inject %s: %v", ev.Type, err)
			}
		}
	}
}

// applyRemoteClipboard installs agent clipboard text locally, recording it so
// the poll loop does not echo the same text straight back.
func (c *Client) applyRemoteClipboard(text string) {
	c.clipMu.Lock()
	c.clipPrimed = true
	c.lastClip = text
	c.clipMu.Unlock()

	if c.Clip == nil {
		return
	}
	if err := c.Clip.SetText(text); err != nil {
		log.Printf("clipboard set: %v", err)
	}
}

// noteLocalClipboard records locally observed clipboard text and reports
// whether it should be sent to the agent. The first observation only primes
// the baseline: clipboard contents that predate the session are never shipped.
func (c *Client) noteLocalClipboard(text string) bool {
	c.clipMu.Lock()
	defer c.clipMu.Unlock()
	if !c.clipPrimed {
		c.clipPrimed = true
		c.lastClip = text
		return false
	}
	if text == c.lastClip {
		return false
	}
	c.lastClip = text
	return true
}

// clipboardLoop polls the OS clipboard and forwards changes to the agent.
// Windows clipboard listeners need a message window, so a 1 s poll keeps this
// simple and cheap (a no-change poll is two syscalls, no copy).
func (c *Client) clipboardLoop(ctx context.Context, conn *websocket.Conn) {
	if c.Clip == nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		text, err := c.Clip.GetText()
		if err != nil {
			continue
		}
		if len(text) > maxClipboardBytes || !c.noteLocalClipboard(text) {
			continue
		}
		if err := conn.Write(ctx, websocket.MessageBinary, encodeClipboard(text)); err != nil {
			return
		}
	}
}

func (c *Client) captureLoop(ctx context.Context, conn *websocket.Conn) error {
	ticker := time.NewTicker(time.Second / time.Duration(c.targetFPS))
	defer ticker.Stop()

	w, h := c.cap.Bounds()
	frameHasher := fnv.New64a()
	var lastHash uint64
	encoder := newWireVideoEncoder(w, h, c.targetFPS, c.frameQuality)
	defer func() { _ = encoder.Close() }()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}

		img, err := c.cap.Capture()
		if err != nil {
			log.Printf("capture: %v", err)
			continue
		}

		nrgba, ok := img.(*image.NRGBA)
		if !ok {
			log.Printf("capture: unexpected image type %T", img)
			continue
		}

		// Hash the full raw frame before encoding. The previous sampled hash was
		// fast, but could miss small cursor/caret/text changes that matter in a
		// remote desktop session.
		frameHasher.Reset()
		pix := nrgba.Pix
		if _, err := frameHasher.Write(pix); err != nil {
			log.Printf("frame hash: %v", err)
			continue
		}
		if h := frameHasher.Sum64(); h == lastHash {
			continue
		} else {
			lastHash = h
		}

		// The display resolution can change mid-session (e.g. a monitor
		// reconfiguration). The encoder's dimensions are frozen at
		// construction, so a mismatch here would make every Encode call fail
		// forever. Rebuild the encoder for the new size before encoding.
		fw, fh := nrgba.Rect.Dx(), nrgba.Rect.Dy()
		if fw != w || fh != h {
			_ = encoder.Close()
			w, h = fw, fh
			encoder = newWireVideoEncoder(w, h, c.targetFPS, c.frameQuality)
		}

		messages, err := encoder.Encode(img)
		if err != nil {
			log.Printf("video encode: %v", err)
			if _, ok := encoder.(*webpVideoEncoder); !ok {
				_ = encoder.Close()
				encoder = newWebPVideoEncoder(w, h, c.frameQuality)
			}
			// The current frame was dropped (encode failed or the encoder
			// was just swapped to a fallback that hasn't seen it yet).
			// Reset lastHash so the next loop iteration re-encodes the
			// current frame instead of waiting for the screen to change.
			lastHash = 0
			continue
		}

		for _, msg := range messages {
			if err := conn.Write(ctx, websocket.MessageBinary, msg); err != nil {
				return fmt.Errorf("write video: %w", err)
			}
		}
	}
}

func readJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	_, b, err := conn.Read(ctx)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
