package main

import (
	"testing"
	"time"
)

func TestEnvDuration(t *testing.T) {
	const name = "TEST_ENV_DURATION"
	def := 10 * time.Minute

	tests := map[string]time.Duration{
		"":     def,
		"30s":  30 * time.Second,
		"2h":   2 * time.Hour,
		"-5m":  def, // non-positive rejected
		"0s":   def,
		"soon": def, // unparseable
		"90":   def, // missing unit
	}
	for val, want := range tests {
		t.Setenv(name, val)
		if got := envDuration(name, def); got != want {
			t.Errorf("envDuration(%q) = %v, want %v", val, got, want)
		}
	}
}

func TestEnvInt(t *testing.T) {
	const name = "TEST_ENV_INT"

	tests := map[string]int{
		"":    8,
		"12":  12,
		"0":   8, // non-positive rejected
		"-3":  8,
		"1.5": 8, // unparseable
		"x":   8,
	}
	for val, want := range tests {
		t.Setenv(name, val)
		if got := envInt(name, 8); got != want {
			t.Errorf("envInt(%q) = %d, want %d", val, got, want)
		}
	}
}

func TestEnvInt64(t *testing.T) {
	const name = "TEST_ENV_INT64"
	def := int64(10 << 20)

	tests := map[string]int64{
		"":         def,
		"1048576":  1 << 20,
		"0":        def,
		"-1":       def,
		"nonsense": def,
	}
	for val, want := range tests {
		t.Setenv(name, val)
		if got := envInt64(name, def); got != want {
			t.Errorf("envInt64(%q) = %d, want %d", val, got, want)
		}
	}
}
