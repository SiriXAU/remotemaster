package relay

import (
	"log"
	"os"
	"strconv"
)

// envClampedInt reads an integer from the environment, clamping it to
// [lo, hi]. Unset or malformed values fall back to def so a bad variable can
// never stall the capture loop (e.g. FPS of 0) or produce an invalid encoder
// quality.
func envClampedInt(name string, def, lo, hi int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("config: invalid %s=%q, using default %d", name, v, def)
		return def
	}
	if n < lo {
		log.Printf("config: %s=%d below minimum, clamping to %d", name, n, lo)
		return lo
	}
	if n > hi {
		log.Printf("config: %s=%d above maximum, clamping to %d", name, n, hi)
		return hi
	}
	return n
}
