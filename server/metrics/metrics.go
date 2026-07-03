// Package metrics exposes relay counters in Prometheus text exposition
// format. Counters are plain atomics so the hot relay path pays one atomic
// add per message — no external dependency, no registry, no locking.
package metrics

import (
	"fmt"
	"net/http"
	"sync/atomic"
)

var (
	// SessionsCreated counts client registrations (codes issued).
	SessionsCreated atomic.Int64
	// SessionsJoined counts successful agent joins.
	SessionsJoined atomic.Int64
	// JoinFailures counts failed join attempts (bad or unknown codes).
	JoinFailures atomic.Int64
	// JoinBlocked counts joins rejected by the rate limiter.
	JoinBlocked atomic.Int64

	// Bytes relayed per direction, measured at the bridge.
	BytesClientToAgent atomic.Int64
	BytesAgentToClient atomic.Int64
	// MessagesRelayed counts WebSocket messages pumped in both directions.
	MessagesRelayed atomic.Int64
)

// Handler serves GET /metrics. counts reports the current number of pending
// and active sessions (a snapshot from the session store).
func Handler(counts func() (pending, active int)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		pending, active := 0, 0
		if counts != nil {
			pending, active = counts()
		}

		writeMetric(w, "remotemaster_sessions_pending", "gauge",
			"Sessions waiting for an agent to join.", int64(pending))
		writeMetric(w, "remotemaster_sessions_active", "gauge",
			"Sessions with an agent attached.", int64(active))
		writeMetric(w, "remotemaster_sessions_created_total", "counter",
			"Client registrations (codes issued).", SessionsCreated.Load())
		writeMetric(w, "remotemaster_sessions_joined_total", "counter",
			"Successful agent joins.", SessionsJoined.Load())
		writeMetric(w, "remotemaster_join_failures_total", "counter",
			"Join attempts with a bad or unknown code.", JoinFailures.Load())
		writeMetric(w, "remotemaster_join_blocked_total", "counter",
			"Join attempts rejected by the rate limiter.", JoinBlocked.Load())
		writeMetric(w, "remotemaster_relay_messages_total", "counter",
			"WebSocket messages relayed in both directions.", MessagesRelayed.Load())

		fmt.Fprintf(w, "# HELP remotemaster_relay_bytes_total Payload bytes relayed between client and agent.\n")
		fmt.Fprintf(w, "# TYPE remotemaster_relay_bytes_total counter\n")
		fmt.Fprintf(w, "remotemaster_relay_bytes_total{direction=\"client_to_agent\"} %d\n", BytesClientToAgent.Load())
		fmt.Fprintf(w, "remotemaster_relay_bytes_total{direction=\"agent_to_client\"} %d\n", BytesAgentToClient.Load())
	})
}

func writeMetric(w http.ResponseWriter, name, typ, help string, v int64) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %d\n", name, help, name, typ, name, v)
}
