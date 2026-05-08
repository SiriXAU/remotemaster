package relay

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"log"
	"time"

	"nhooyr.io/websocket"

	"github.com/sirixau/remotemaster/client/capture"
	"github.com/sirixau/remotemaster/client/input"
)

const (
	frameQuality = 65
	targetFPS    = 15
	dialTimeout  = 10 * time.Second
)

// wireMsg covers all message types in both directions.
type wireMsg struct {
	Type string `json:"type"`
	// Server → client control
	Code string `json:"code,omitempty"`
	Msg  string `json:"msg,omitempty"`
	// Client → server (frame)
	W    int    `json:"w,omitempty"`
	H    int    `json:"h,omitempty"`
	Data string `json:"data,omitempty"`
	// Agent → client (input events, forwarded raw by relay)
	X   int    `json:"x,omitempty"`
	Y   int    `json:"y,omitempty"`
	Btn string `json:"btn,omitempty"`
	Dx  int    `json:"dx,omitempty"`
	Dy  int    `json:"dy,omitempty"`
	VK  int    `json:"vk,omitempty"`
	Key string `json:"key,omitempty"`
}

// Client manages the WebSocket relay connection and drives the capture loop.
type Client struct {
	serverURL string
	cap       capture.Capturer
	inj       input.Injector
	onCode    func(code string)
	onConnect func()
	onDisconn func()
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
			log.Printf("relay: disconnected (%v), retrying in %s", err, delay)
			if c.onDisconn != nil {
				c.onDisconn()
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
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.CloseNow()

	// Wait for the "registered" message to get our session code.
	var reg wireMsg
	if err := readJSON(ctx, conn, &reg); err != nil {
		return fmt.Errorf("read registered: %w", err)
	}
	if reg.Type != "registered" {
		return fmt.Errorf("expected 'registered', got %q", reg.Type)
	}
	if c.onCode != nil {
		c.onCode(reg.Code)
	}
	log.Printf("relay: session code = %s", reg.Code)

	// Wait for "agent_connected" before starting the capture loop.
	agentCh := make(chan struct{}, 1)
	inputCh := make(chan wireMsg, 64)

	// Read pump — handles all inbound messages.
	go c.readPump(ctx, conn, agentCh, inputCh)

	// Input injection — drain inputCh and inject events.
	go c.injectLoop(ctx, inputCh)

	// Wait for agent.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-agentCh:
	}

	if c.onConnect != nil {
		c.onConnect()
	}

	return c.captureLoop(ctx, conn)
}

func (c *Client) readPump(ctx context.Context, conn *websocket.Conn, agentCh chan struct{}, inputCh chan wireMsg) {
	for {
		var m wireMsg
		if err := readJSON(ctx, conn, &m); err != nil {
			return
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
		default:
			// Treat unknown messages as potential input events.
			select {
			case inputCh <- m:
			default:
				// Drop if channel is full (shouldn't happen at 15fps)
			}
		}
	}
}

func (c *Client) injectLoop(ctx context.Context, ch chan wireMsg) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-ch:
			if c.inj == nil {
				continue
			}
			ev := input.Event{
				Type: m.Type,
				X:    m.X,
				Y:    m.Y,
				Btn:  m.Btn,
				Dx:   m.Dx,
				Dy:   m.Dy,
				VK:   m.VK,
				Key:  m.Key,
			}
			if err := c.inj.Inject(ev); err != nil {
				log.Printf("inject %s: %v", ev.Type, err)
			}
		}
	}
}

func (c *Client) captureLoop(ctx context.Context, conn *websocket.Conn) error {
	ticker := time.NewTicker(time.Second / targetFPS)
	defer ticker.Stop()

	w, h := c.cap.Bounds()
	var lastHash [16]byte

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

		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: frameQuality}); err != nil {
			log.Printf("jpeg encode: %v", err)
			continue
		}

		// Skip unchanged frames.
		hash := md5.Sum(buf.Bytes())
		if hash == lastHash {
			continue
		}
		lastHash = hash

		m := wireMsg{
			Type: "frame",
			W:    w,
			H:    h,
			Data: base64.StdEncoding.EncodeToString(buf.Bytes()),
		}
		b, _ := json.Marshal(m)
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			return fmt.Errorf("write frame: %w", err)
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
