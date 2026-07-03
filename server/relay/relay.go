package relay

import (
	"context"
	"io"
	"log"

	"nhooyr.io/websocket"

	"github.com/sirixau/remotemaster/server/session"
)

// maxMessageBytes is the per-message read limit applied to both sides of the
// relay. The default nhooyr.io/websocket limit is 32 KiB, which is too small
// for a single WebP frame (or an H.264 access unit). 10 MiB is enough headroom
// for a 4K screen at the chosen encoder quality.
const maxMessageBytes = 10 * 1024 * 1024

// Bridge pumps messages bidirectionally between a session's client and agent.
// It blocks until one side closes, then closes both connections.
// onDone is called when the bridge shuts down.
func Bridge(ctx context.Context, sess *session.Session, onDone func()) {
	defer onDone()

	sess.ClientConn.SetReadLimit(maxMessageBytes)
	sess.AgentConn.SetReadLimit(maxMessageBytes)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		pump(ctx, cancel, sess.AgentConn, sess.ClientConn, "agent→client")
	}()

	pump(ctx, cancel, sess.ClientConn, sess.AgentConn, "client→agent")
	<-done // wait for both directions to finish

	sess.ClientConn.Close(websocket.StatusNormalClosure, "session ended")
	sess.AgentConn.Close(websocket.StatusNormalClosure, "session ended")
}

func pump(ctx context.Context, cancel context.CancelFunc, src, dst *websocket.Conn, label string) {
	defer cancel()
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
		if _, err := io.Copy(w, r); err != nil {
			log.Printf("relay %s copy: %v", label, err)
			w.Close()
			return
		}
		if err := w.Close(); err != nil {
			log.Printf("relay %s flush: %v", label, err)
			return
		}
	}
}
