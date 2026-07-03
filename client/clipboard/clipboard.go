// Package clipboard provides access to the OS text clipboard for session
// clipboard sync. Platform implementations live behind build tags, mirroring
// the capture and input packages.
package clipboard

// Clipboard reads and writes the OS text clipboard.
type Clipboard interface {
	// GetText returns the current clipboard text, or "" (no error) when the
	// clipboard is empty or holds a non-text format.
	GetText() (string, error)
	SetText(text string) error
}
