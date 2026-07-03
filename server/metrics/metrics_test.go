package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerExposition(t *testing.T) {
	SessionsCreated.Store(3)
	SessionsJoined.Store(2)
	JoinFailures.Store(5)
	JoinBlocked.Store(1)
	BytesClientToAgent.Store(1024)
	BytesAgentToClient.Store(64)
	MessagesRelayed.Store(7)

	h := Handler(func() (int, int) { return 4, 9 })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain exposition format", ct)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"remotemaster_sessions_pending 4",
		"remotemaster_sessions_active 9",
		"remotemaster_sessions_created_total 3",
		"remotemaster_sessions_joined_total 2",
		"remotemaster_join_failures_total 5",
		"remotemaster_join_blocked_total 1",
		"remotemaster_relay_messages_total 7",
		`remotemaster_relay_bytes_total{direction="client_to_agent"} 1024`,
		`remotemaster_relay_bytes_total{direction="agent_to_client"} 64`,
		"# TYPE remotemaster_sessions_pending gauge",
		"# TYPE remotemaster_sessions_created_total counter",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q\n%s", want, body)
		}
	}
}

func TestHandlerNilCounts(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(nil).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "remotemaster_sessions_pending 0") {
		t.Fatal("nil counts func should report zero gauges")
	}
}
