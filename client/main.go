//go:build windows

package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/sirixau/remotemaster/client/capture"
	"github.com/sirixau/remotemaster/client/input"
	"github.com/sirixau/remotemaster/client/relay"
	"github.com/sirixau/remotemaster/client/ui"
)

// RelayServer is the relay server URL, injected at build time with:
//
//	-ldflags "-X main.RelayServer=wss://yourdomain.com"
var RelayServer = "ws://localhost:8080"

func main() {
	log.SetFlags(log.Ltime)

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
		RelayServer,
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
