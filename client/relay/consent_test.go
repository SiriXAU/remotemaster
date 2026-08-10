package relay

import (
	"context"
	"testing"
	"time"
)

func TestRequestConsent(t *testing.T) {
	t.Setenv("REMOTEMASTER_AUTO_CONSENT", "")

	for _, tt := range []struct {
		name string
		ask  func(context.Context, string) bool
		want bool
	}{
		{"granted", func(context.Context, string) bool { return true }, true},
		{"denied", func(context.Context, string) bool { return false }, false},
		{"missing prompt denies", nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := &Client{RequestConsent: tt.ask}
			if got := c.requestConsent(context.Background(), "203.0.113.9"); got != tt.want {
				t.Fatalf("requestConsent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRequestConsentContextCancellationDenies(t *testing.T) {
	t.Setenv("REMOTEMASTER_AUTO_CONSENT", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &Client{RequestConsent: func(ctx context.Context, _ string) bool {
		<-ctx.Done()
		return true
	}}
	if c.requestConsent(ctx, "") {
		t.Fatal("cancelled consent prompt granted control")
	}
}

func TestRequestConsentTimeoutDenies(t *testing.T) {
	t.Setenv("REMOTEMASTER_AUTO_CONSENT", "")
	c := &Client{RequestConsent: func(ctx context.Context, _ string) bool {
		<-ctx.Done()
		return true
	}}
	if c.requestConsentWithin(context.Background(), "", time.Millisecond) {
		t.Fatal("timed-out consent prompt granted control")
	}
}

func TestAutoConsent(t *testing.T) {
	t.Setenv("REMOTEMASTER_AUTO_CONSENT", "1")
	c := &Client{RequestConsent: func(context.Context, string) bool {
		t.Fatal("auto-consent should not open a prompt")
		return false
	}}
	if !c.requestConsent(context.Background(), "") {
		t.Fatal("auto-consent did not grant control")
	}
}

func TestConsentMessage(t *testing.T) {
	for _, granted := range []bool{true, false} {
		m := consentMessage(granted)
		if m.Type != "consent" || m.Granted == nil || *m.Granted != granted {
			t.Fatalf("consentMessage(%v) = %#v", granted, m)
		}
	}
}

func TestRequestConsentReturnsPromptResult(t *testing.T) {
	t.Setenv("REMOTEMASTER_AUTO_CONSENT", "")
	c := &Client{RequestConsent: func(context.Context, string) bool {
		time.Sleep(time.Millisecond)
		return true
	}}
	if !c.requestConsent(context.Background(), "") {
		t.Fatal("prompt result was not returned")
	}
}
