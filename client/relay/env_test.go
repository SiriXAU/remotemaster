package relay

import "testing"

func TestEnvClampedInt(t *testing.T) {
	const name = "TEST_ENV_CLAMPED"

	tests := map[string]int{
		"":    15, // unset → default
		"30":  30,
		"1":   1,
		"60":  60,
		"0":   1,  // below range → clamped
		"-5":  1,  // below range → clamped
		"200": 60, // above range → clamped
		"abc": 15, // unparseable → default
		"1.5": 15,
	}
	for val, want := range tests {
		t.Setenv(name, val)
		if got := envClampedInt(name, 15, 1, 60); got != want {
			t.Errorf("envClampedInt(%q) = %d, want %d", val, got, want)
		}
	}
}
