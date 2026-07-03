// Package audit emits structured JSON-lines records of who connected to
// which session code, when, and for how long. It answers the after-the-fact
// questions ("who controlled this machine on Tuesday?") that the human-oriented
// server log cannot, and is designed to be shipped to a log pipeline as-is.
package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"
)

// Event is one audit record. Fields are omitted when empty so each line only
// carries what is meaningful for its event type.
type Event struct {
	Time  time.Time `json:"time"`
	Event string    `json:"event"`
	Code  string    `json:"code,omitempty"`
	IP    string    `json:"ip,omitempty"`
	// Reason qualifies rejections (e.g. rate_limited, bad_code, bad_token).
	Reason string `json:"reason,omitempty"`
	// DurationSeconds is how long the session was pending (on join) or
	// active (on end).
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
}

// Event type names.
const (
	EventSessionCreated = "session_created"
	EventAgentJoined    = "agent_joined"
	EventJoinRejected   = "join_rejected"
	EventSessionEnded   = "session_ended"
	EventClientLost     = "client_lost"
)

var (
	mu  sync.Mutex
	out io.Writer // nil = disabled
)

// Configure sets the audit destination from the AUDIT_LOG value: empty
// disables auditing, "stderr"/"stdout" write to those streams, anything else
// is opened (append/create) as a file path. Returns an error only for an
// unopenable file.
func Configure(dest string) error {
	switch dest {
	case "":
		SetOutput(nil)
	case "stderr":
		SetOutput(os.Stderr)
	case "stdout":
		SetOutput(os.Stdout)
	default:
		f, err := os.OpenFile(dest, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("audit log %s: %w", dest, err)
		}
		SetOutput(f)
	}
	return nil
}

// SetOutput directs audit records to w; nil disables auditing.
func SetOutput(w io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	out = w
}

// Enabled reports whether audit records are being written anywhere.
func Enabled() bool {
	mu.Lock()
	defer mu.Unlock()
	return out != nil
}

// Log writes one record as a JSON line, stamping the time. No-op when
// disabled, so call sites never need to guard.
func Log(e Event) {
	mu.Lock()
	defer mu.Unlock()
	if out == nil {
		return
	}
	e.Time = time.Now().UTC()
	b, err := json.Marshal(e)
	if err != nil {
		log.Printf("audit: marshal: %v", err)
		return
	}
	if _, err := out.Write(append(b, '\n')); err != nil {
		log.Printf("audit: write: %v", err)
	}
}
