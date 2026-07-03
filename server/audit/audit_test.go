package audit

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogWritesJSONLines(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	defer SetOutput(nil)

	Log(Event{Event: EventAgentJoined, Code: "123456", IP: "203.0.113.7", DurationSeconds: 4.2})
	Log(Event{Event: EventJoinRejected, IP: "203.0.113.8", Reason: "bad_token"})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), buf.String())
	}

	var first Event
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("first line is not JSON: %v", err)
	}
	if first.Event != EventAgentJoined || first.Code != "123456" ||
		first.IP != "203.0.113.7" || first.DurationSeconds != 4.2 {
		t.Fatalf("first event round-trip mismatch: %+v", first)
	}
	if first.Time.IsZero() || time.Since(first.Time) > time.Minute {
		t.Fatalf("timestamp not stamped: %v", first.Time)
	}

	// Empty fields are omitted, not emitted as zero values.
	if strings.Contains(lines[1], `"code"`) || strings.Contains(lines[1], `"duration_seconds"`) {
		t.Fatalf("rejection line carries empty fields: %s", lines[1])
	}
}

func TestLogDisabledIsNoop(t *testing.T) {
	SetOutput(nil)
	if Enabled() {
		t.Fatal("Enabled() = true after SetOutput(nil)")
	}
	Log(Event{Event: EventSessionCreated, Code: "654321"}) // must not panic
}

func TestConfigure(t *testing.T) {
	defer SetOutput(nil)

	if err := Configure(""); err != nil || Enabled() {
		t.Fatalf("Configure(\"\") = %v, enabled=%v; want disabled", err, Enabled())
	}
	if err := Configure("stderr"); err != nil || !Enabled() {
		t.Fatalf("Configure(stderr) = %v, enabled=%v; want enabled", err, Enabled())
	}

	path := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := Configure(path); err != nil {
		t.Fatalf("Configure(file) = %v", err)
	}
	Log(Event{Event: EventSessionCreated, Code: "111111"})

	if err := Configure(filepath.Join(t.TempDir(), "missing-dir", "audit.jsonl")); err == nil {
		t.Fatal("Configure with unopenable path did not error")
	}
}
