package main

import (
	"net/http"
	"testing"
	"time"
)

func TestIsSixDigitCode(t *testing.T) {
	tests := map[string]bool{
		"123456":  true,
		"000000":  true,
		"12345":   false,
		"1234567": false,
		"12345a":  false,
	}

	for code, want := range tests {
		if got := isSixDigitCode(code); got != want {
			t.Fatalf("isSixDigitCode(%q) = %v, want %v", code, got, want)
		}
	}
}

func TestAttemptLimiterBlocksAfterLimit(t *testing.T) {
	limiter := newAttemptLimiter(2, time.Minute, time.Minute)
	if !limiter.Allow("198.51.100.10") {
		t.Fatal("first attempt blocked")
	}
	if !limiter.Allow("198.51.100.10") {
		t.Fatal("second attempt blocked")
	}
	if limiter.Allow("198.51.100.10") {
		t.Fatal("third attempt allowed")
	}
	if !limiter.Allow("198.51.100.11") {
		t.Fatal("different requester blocked")
	}
}

func TestAllowWebSocketOrigin(t *testing.T) {
	req, err := http.NewRequest("GET", "http://support.example/ws/agent", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Host = "support.example"
	req.Header.Set("Origin", "http://support.example")
	if !allowWebSocketOrigin(req) {
		t.Fatal("same-host origin rejected")
	}

	req.Header.Set("Origin", "http://evil.example")
	if allowWebSocketOrigin(req) {
		t.Fatal("cross-host origin accepted")
	}
}
