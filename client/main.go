//go:build windows

package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sirixau/remotemaster/client/capture"
	"github.com/sirixau/remotemaster/client/input"
	"github.com/sirixau/remotemaster/client/relay"
	"github.com/sirixau/remotemaster/client/ui"
)

// RelayServer is the compiled-in default, overridable at build time with:
//
//	-ldflags "-X main.RelayServer=wss://yourdomain.com"
var RelayServer = "ws://localhost:8080"

// resolveServerURL returns the relay server URL from the first matching source:
//  1. First command-line argument (if it starts with "ws")
//  2. server.txt file in the same directory as the executable
//  3. Compiled-in RelayServer
func resolveServerURL() string {
	if len(os.Args) > 1 && strings.HasPrefix(os.Args[1], "ws") {
		return os.Args[1]
	}
	if exe, err := os.Executable(); err == nil {
		data, err := os.ReadFile(filepath.Join(filepath.Dir(exe), "server.txt"))
		if err == nil {
			if url := strings.TrimSpace(string(data)); url != "" {
				return url
			}
		}
	}
	return RelayServer
}

func main() {
	log.SetFlags(log.Ltime)

	serverURL := resolveServerURL()
	log.Printf("relay server: %s", serverURL)

	cap, err := capture.New()
	if err != nil {
		log.Fatalf("screen capture init: %v", err)
	}
	defer cap.Close()

	inj, err := input.NewInjector()
	if err != nil {
		log.Fatalf("input injector init: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Graceful shutdown on Ctrl-C / OS signal
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		cancel()
	}()

	client := relay.New(
		serverURL,
		cap,
		inj,
		func(code string) {
			ui.SetCode(code)
		},
		func() {
			log.Println("agent connected")
		},
		func() {
			log.Println("agent disconnected")
			ui.SetCode("------")
		},
	)

	// Connect to relay in the background.
	go client.Run(ctx)

	// Run the native window on the main goroutine (required by Win32).
	// The window shows the session code and an End Session button.
	if err := ui.Run("", cancel); err != nil {
		log.Fatalf("window: %v", err)
	}

	cancel()
}
