package input

import "testing"

func TestScaleToAbsolute(t *testing.T) {
	tests := []struct {
		name                string
		x, y, width, height int
		wantX, wantY        int32
	}{
		{name: "origin", width: 1920, height: 1080, wantX: 0, wantY: 0},
		{name: "bottom right", x: 1919, y: 1079, width: 1920, height: 1080, wantX: 65535, wantY: 65535},
		{name: "center after resize", x: 640, y: 360, width: 1280, height: 720, wantX: 32793, wantY: 32813},
		{name: "clamps stale coordinates", x: 1920, y: -1, width: 1280, height: 720, wantX: 65535, wantY: 0},
		{name: "one pixel display", x: 100, y: 100, width: 1, height: 1, wantX: 0, wantY: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotX, gotY := scaleToAbsolute(tt.x, tt.y, tt.width, tt.height)
			if gotX != tt.wantX || gotY != tt.wantY {
				t.Fatalf("scaleToAbsolute(%d, %d, %d, %d) = (%d, %d), want (%d, %d)", tt.x, tt.y, tt.width, tt.height, gotX, gotY, tt.wantX, tt.wantY)
			}
		})
	}
}
