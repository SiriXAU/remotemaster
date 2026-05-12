#!/usr/bin/env bash
# Cross-compiles the Windows client EXE from Linux.
# Requires: Go 1.22+, mingw-w64 (github.com/chai2010/webp uses CGo)
#
# Usage:
#   ./build/build-client.sh
#   SERVER_URL=wss://yourdomain.com ./build/build-client.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT/client"

SERVER_URL="${SERVER_URL:-ws://localhost:8080}"
CC="${CC:-x86_64-w64-mingw32-gcc}"
CGO_ENABLED="${CGO_ENABLED:-1}"

echo "Building Windows client (server: $SERVER_URL)..."
CC="$CC" GOOS=windows GOARCH=amd64 CGO_ENABLED="$CGO_ENABLED" \
  go build \
  -ldflags="-H windowsgui -s -w -extldflags=-static -X main.RelayServer=${SERVER_URL}" \
  -o "$REPO_ROOT/dist/remotemaster-client.exe" \
  .
echo "Done: dist/remotemaster-client.exe"
ls -lh "$REPO_ROOT/dist/remotemaster-client.exe"
