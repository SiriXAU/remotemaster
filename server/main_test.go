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

	// Only failures count; a fresh requester is never blocked.
	if limiter.Blocked("198.51.100.10") {
		t.Fatal("fresh requester blocked")
	}

	// Up to the limit of failures is tolerated.
	limiter.Fail("198.51.100.10")
	limiter.Fail("198.51.100.10")
	if limiter.Blocked("198.51.100.10") {
		t.Fatal("blocked at limit, want tolerated")
	}

	// One failure past the limit trips the block.
	limiter.Fail("198.51.100.10")
	if !limiter.Blocked("198.51.100.10") {
		t.Fatal("not blocked after exceeding limit")
	}

	// A different requester is unaffected.
	if limiter.Blocked("198.51.100.11") {
		t.Fatal("unrelated requester blocked")
	}
}

func TestClientIPIgnoresForwardedForByDefault(t *testing.T) {
	req, err := http.NewRequest("GET", "http://support.example/ws/agent", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.RemoteAddr = "203.0.113.7:54321"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	trustProxyHeaders = false
	if got := clientIP(req); got != "203.0.113.7" {
		t.Fatalf("clientIP with untrusted proxy = %q, want RemoteAddr host", got)
	}

	trustProxyHeaders = true
	defer func() { trustProxyHeaders = false }()
	if got := clientIP(req); got != "1.2.3.4" {
		t.Fatalf("clientIP with trusted proxy = %q, want forwarded value", got)
	}
}

func TestIsValidHost(t *testing.T) {
	tests := map[string]bool{
		"support.example":      true,
		"support.example:8080": true,
		"10.0.0.1:443":         true,
		"[::1]:8080":           true,
		"evil'; calc; '":       false,
		"host name":            false,
		"":                     false,
	}
	for host, want := range tests {
		if got := isValidHost(host); got != want {
			t.Fatalf("isValidHost(%q) = %v, want %v", host, got, want)
		}
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
