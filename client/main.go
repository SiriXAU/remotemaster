//go:build windows

package main

import (
	"context"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sirixau/remotemaster/client/capture"
	"github.com/sirixau/remotemaster/client/clipboard"
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
			// Tolerate a UTF-8 BOM: Windows PowerShell 5.1 writes one with
			// -Encoding UTF8, and it is not whitespace so TrimSpace keeps it.
			text := strings.TrimPrefix(string(data), "\ufeff")
			if url := strings.TrimSpace(text); url != "" {
				return url
			}
		}
	}
	return RelayServer
}

func main() {
	log.SetFlags(log.Ltime)

	// The client is built with -H windowsgui, so stderr goes nowhere. Tee
	// logs to client.log next to the EXE so encoder/connection problems can
	// be diagnosed in the field. Truncated on every start to stay small.
	if exe, err := os.Executable(); err == nil {
		logPath := filepath.Join(filepath.Dir(exe), "client.log")
		if f, err := os.Create(logPath); err == nil {
			// The file must come first: MultiWriter stops at the first write
			// error, and under -H windowsgui os.Stderr is an invalid handle
			// whose writes always fail.
			log.SetOutput(io.MultiWriter(f, os.Stderr))
		}
	}

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
			ui.SetStatus("Agent connected — your screen is being shared")
		},
		func() {
			log.Println("agent disconnected")
			ui.SetCode("------")
		},
	)
	client.OnConnFail = func() {
		ui.SetCode("NOCONN")
	}

	if clip, err := clipboard.New(); err != nil {
		log.Printf("clipboard sync unavailable: %v", err)
	} else {
		client.Clip = clip
	}

	// Connect to relay in the background.
	go client.Run(ctx)

	// Run the native window on the main goroutine (required by Win32).
	// The window shows the session code and an End Session button.
	if err := ui.Run("", cancel); err != nil {
		log.Fatalf("window: %v", err)
	}

	cancel()
}
