package relay

import (
	"context"
	"io"
	"log"
	"sync/atomic"

	"nhooyr.io/websocket"

	"github.com/sirixau/remotemaster/server/metrics"
	"github.com/sirixau/remotemaster/server/session"
)

// MaxMessageBytes is the per-message read limit applied to both sides of the
// relay. The default nhooyr.io/websocket limit is 32 KiB, which is too small
// for a single full WebP frame. 10 MiB is enough headroom
// for a 4K screen at the chosen encoder quality. Overridable at startup
// (MAX_MESSAGE_BYTES); set before any bridge starts, not while serving.
var MaxMessageBytes int64 = 10 * 1024 * 1024

// copyBufferSize matches io.Copy's default staging buffer. Keeping that size
// preserves the existing WebSocket fragmentation while limiting retained relay
// scratch space to 64 KiB across the two pumps in an active session.
const copyBufferSize = 32 * 1024

// Bridge pumps messages bidirectionally between a session's client and agent.
// It blocks until one side closes, then closes both connections.
// onDone is called when the bridge shuts down.
func Bridge(ctx context.Context, sess *session.Session, onDone func()) {
	defer onDone()

	sess.ClientConn.SetReadLimit(MaxMessageBytes)
	sess.AgentConn.SetReadLimit(MaxMessageBytes)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		pump(ctx, cancel, sess.AgentConn, sess.ClientConn, "agent→client", &metrics.BytesAgentToClient)
	}()

	pump(ctx, cancel, sess.ClientConn, sess.AgentConn, "client→agent", &metrics.BytesClientToAgent)
	<-done // wait for both directions to finish

	sess.ClientConn.Close(websocket.StatusNormalClosure, "session ended")
	sess.AgentConn.Close(websocket.StatusNormalClosure, "session ended")
}

func pump(ctx context.Context, cancel context.CancelFunc, src, dst *websocket.Conn, label string, bytesCounter *atomic.Int64) {
	defer cancel()
	copyBuffer := make([]byte, copyBufferSize)
	for {
		mt, r, err := src.Reader(ctx)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("relay %s read: %v", label, err)
			}
			return
		}
		w, err := dst.Writer(ctx, mt)
		if err != nil {
			log.Printf("relay %s writer: %v", label, err)
			return
		}
		n, err := copyMessage(w, r, copyBuffer)
		bytesCounter.Add(n)
		if err != nil {
			log.Printf("relay %s copy: %v", label, err)
			w.Close()
			return
		}
		if err := w.Close(); err != nil {
			log.Printf("relay %s flush: %v", label, err)
			return
		}
		metrics.MessagesRelayed.Add(1)
	}
}

func copyMessage(dst io.Writer, src io.Reader, buffer []byte) (int64, error) {
	return io.CopyBuffer(dst, src, buffer)
}
