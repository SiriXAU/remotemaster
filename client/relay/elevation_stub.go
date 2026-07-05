//go:build !windows

package relay

import (
	"context"

	"nhooyr.io/websocket"
)

// watchElevatedFocus is Windows-only (UIPI); no-op elsewhere so the package
// stays testable on development hosts.
func (c *Client) watchElevatedFocus(ctx context.Context, conn *websocket.Conn) {}
