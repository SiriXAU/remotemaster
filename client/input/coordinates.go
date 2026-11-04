package input

// scaleToAbsolute maps a pixel coordinate in a screen of width and height to
// the 0–65535 coordinate space used by SendInput. Invalid dimensions are
// treated as a one-pixel screen so callers cannot divide by zero.
func scaleToAbsolute(x, y, width, height int) (int32, int32) {
	width = max(width, 1)
	height = max(height, 1)
	x = clamp(x, 0, width-1)
	y = clamp(y, 0, height-1)
	return int32(x * 65535 / max(width-1, 1)), int32(y * 65535 / max(height-1, 1))
}

func clamp(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
