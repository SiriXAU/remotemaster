package main

import (
	"log"
	"os"
	"strconv"
	"time"
)

// Env-configurable operational limits. Each helper falls back to the given
// default (and logs) when the variable is unset, malformed, or non-positive,
// so a typo in deployment config can never turn a safety limit off.

func envDuration(name string, def time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		log.Printf("config: invalid %s=%q, using default %s", name, v, def)
		return def
	}
	return d
}

func envInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		log.Printf("config: invalid %s=%q, using default %d", name, v, def)
		return def
	}
	return n
}

func envInt64(name string, def int64) int64 {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		log.Printf("config: invalid %s=%q, using default %d", name, v, def)
		return def
	}
	return n
}
