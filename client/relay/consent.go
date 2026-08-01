package relay

import (
	"context"
	"os"
	"time"
)

// consentTimeout bounds how long an agent can wait for the person at the
// controlled machine to decide. It is deliberately client-enforced: an
// unresponsive client must never become remotely controllable by default.
const consentTimeout = 30 * time.Second

// requestConsent asks the configured UI for permission. A missing UI is a
// denial, which keeps headless or partially configured clients safe by
// default. REMOTEMASTER_AUTO_CONSENT=1 is the explicit opt-out for unattended
// kiosk/self-support deployments.
func (c *Client) requestConsent(ctx context.Context, agentIP string) bool {
	return c.requestConsentWithin(ctx, agentIP, consentTimeout)
}

func (c *Client) requestConsentWithin(ctx context.Context, agentIP string, timeout time.Duration) bool {
	if os.Getenv("REMOTEMASTER_AUTO_CONSENT") == "1" {
		return true
	}
	if c.RequestConsent == nil {
		return false
	}

	promptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	answer := make(chan bool, 1)
	go func() { answer <- c.RequestConsent(promptCtx, agentIP) }()

	select {
	case granted := <-answer:
		return granted
	case <-promptCtx.Done():
		return false
	}
}

func consentMessage(granted bool) ctrlMsg {
	return ctrlMsg{Type: "consent", Granted: &granted}
}
