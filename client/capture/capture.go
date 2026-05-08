package capture

import "image"

// Capturer captures the primary screen as an image.
type Capturer interface {
	Capture() (image.Image, error)
	// Bounds returns the screen dimensions without capturing.
	Bounds() (w, h int)
	Close()
}
