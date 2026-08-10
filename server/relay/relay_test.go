package relay

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"

	"github.com/sirixau/remotemaster/server/metrics"
	"github.com/sirixau/remotemaster/server/session"
)

func TestBridgeRelaysMessagesLargerThanCopyBuffer(t *testing.T) {
	clientConn, clientPeer := newWebSocketPair(t)
	agentConn, agentPeer := newWebSocketPair(t)
	clientPeer.SetReadLimit(MaxMessageBytes)
	agentPeer.SetReadLimit(MaxMessageBytes)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go Bridge(ctx, &session.Session{
		ClientConn: clientConn,
		AgentConn:  agentConn,
	}, func() {
		close(done)
	})

	clientToAgentBytes := metrics.BytesClientToAgent.Load()
	agentToClientBytes := metrics.BytesAgentToClient.Load()
	messagesRelayed := metrics.MessagesRelayed.Load()

	largeBinary := bytes.Repeat([]byte{0x00, 0x7f, 0x80, 0xff}, copyBufferSize+37)
	assertRelayedMessage(t, ctx, clientPeer, agentPeer, websocket.MessageBinary, largeBinary)

	text := []byte("second message reuses the same pump buffer")
	assertRelayedMessage(t, ctx, clientPeer, agentPeer, websocket.MessageText, text)

	reverseBinary := bytes.Repeat([]byte{0xa5}, 2*copyBufferSize+19)
	assertRelayedMessage(t, ctx, agentPeer, clientPeer, websocket.MessageBinary, reverseBinary)

	if got, want := metrics.BytesClientToAgent.Load()-clientToAgentBytes, int64(len(largeBinary)+len(text)); got != want {
		t.Fatalf("client-to-agent byte count = %d, want %d", got, want)
	}
	if got, want := metrics.BytesAgentToClient.Load()-agentToClientBytes, int64(len(reverseBinary)); got != want {
		t.Fatalf("agent-to-client byte count = %d, want %d", got, want)
	}
	if got, want := metrics.MessagesRelayed.Load()-messagesRelayed, int64(3); got != want {
		t.Fatalf("relayed message count = %d, want %d", got, want)
	}

	if err := clientPeer.CloseNow(); err != nil {
		t.Fatalf("close client peer: %v", err)
	}
	if err := agentPeer.CloseNow(); err != nil {
		t.Fatalf("close agent peer: %v", err)
	}

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("bridge did not stop: %v", ctx.Err())
	}
}

func TestCopyMessageReusesCallerBuffer(t *testing.T) {
	payload := make([]byte, 3*copyBufferSize+17)
	src := &resettableReader{data: payload}
	dst := &countingWriter{}
	buffer := make([]byte, copyBufferSize)

	var copied int64
	var copyErr error
	reusedAllocs := testing.AllocsPerRun(1000, func() {
		src.Reset()
		dst.Reset()
		copied, copyErr = copyMessage(dst, src, buffer)
	})
	if copyErr != nil {
		t.Fatalf("copy with reused buffer: %v", copyErr)
	}
	if copied != int64(len(payload)) || dst.written != len(payload) {
		t.Fatalf("copy with reused buffer wrote %d/%d bytes, want %d", copied, dst.written, len(payload))
	}

	baselineAllocs := testing.AllocsPerRun(1000, func() {
		src.Reset()
		dst.Reset()
		copied, copyErr = io.Copy(dst, src)
	})
	if copyErr != nil {
		t.Fatalf("baseline io.Copy: %v", copyErr)
	}
	if copied != int64(len(payload)) || dst.written != len(payload) {
		t.Fatalf("baseline io.Copy wrote %d/%d bytes, want %d", copied, dst.written, len(payload))
	}

	if reusedAllocs != 0 {
		t.Fatalf("copyMessage allocations = %.2f per message, want 0", reusedAllocs)
	}
	if baselineAllocs < 1 {
		t.Fatalf("baseline io.Copy allocations = %.2f per message, want at least 1", baselineAllocs)
	}
}

func BenchmarkCopyMessage(b *testing.B) {
	payload := make([]byte, 3*copyBufferSize+17)

	b.Run("io.Copy/per-message-buffer", func(b *testing.B) {
		src := &resettableReader{data: payload}
		dst := &countingWriter{}
		b.ReportAllocs()
		b.SetBytes(int64(len(payload)))
		for range b.N {
			src.Reset()
			dst.Reset()
			if _, err := io.Copy(dst, src); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("io.CopyBuffer/reused-32KiB", func(b *testing.B) {
		src := &resettableReader{data: payload}
		dst := &countingWriter{}
		buffer := make([]byte, copyBufferSize)
		b.ReportAllocs()
		b.SetBytes(int64(len(payload)))
		b.ResetTimer()
		for range b.N {
			src.Reset()
			dst.Reset()
			if _, err := copyMessage(dst, src, buffer); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func assertRelayedMessage(t *testing.T, ctx context.Context, src, dst *websocket.Conn, messageType websocket.MessageType, payload []byte) {
	t.Helper()
	if err := src.Write(ctx, messageType, payload); err != nil {
		t.Fatalf("write message: %v", err)
	}
	gotType, gotPayload, err := dst.Read(ctx)
	if err != nil {
		t.Fatalf("read relayed message: %v", err)
	}
	if gotType != messageType {
		t.Fatalf("relayed message type = %v, want %v", gotType, messageType)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Fatalf("relayed payload length = %d, want identical %d-byte payload", len(gotPayload), len(payload))
	}
}

type acceptedWebSocket struct {
	conn *websocket.Conn
	err  error
}

func newWebSocketPair(t *testing.T) (serverConn, peerConn *websocket.Conn) {
	t.Helper()
	accepted := make(chan acceptedWebSocket, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		accepted <- acceptedWebSocket{conn: conn, err: err}
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	peerConn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		server.Close()
		t.Fatalf("dial test WebSocket: %v", err)
	}

	result := <-accepted
	if result.err != nil {
		peerConn.CloseNow()
		server.Close()
		t.Fatalf("accept test WebSocket: %v", result.err)
	}
	serverConn = result.conn

	t.Cleanup(func() {
		peerConn.CloseNow()
		serverConn.CloseNow()
		server.Close()
	})
	return serverConn, peerConn
}

type resettableReader struct {
	data   []byte
	offset int
}

func (r *resettableReader) Read(p []byte) (int, error) {
	if r.offset == len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}

func (r *resettableReader) Reset() {
	r.offset = 0
}

type countingWriter struct {
	written int
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.written += len(p)
	return len(p), nil
}

func (w *countingWriter) Reset() {
	w.written = 0
}
