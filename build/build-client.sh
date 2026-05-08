#!/usr/bin/env bash
# Cross-compiles the Windows client EXE from Linux.
# Requires: Go 1.22+, mingw-w64 (for CGO if needed — not required for pure Go build)
#
# Usage:
#   ./build/build-client.sh
#   SERVER_URL=wss://yourdomain.com ./build/build-client.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT/client"

SERVER_URL="${SERVER_URL:-ws://localhost:8080}"

echo "Building Windows client (server: $SERVER_URL)..."
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
  go build \
  -ldflags="-H windowsgui -s -w -X main.RelayServer=${SERVER_URL}" \
  -o "$REPO_ROOT/dist/remotemaster-client.exe" \
  .
echo "Done: dist/remotemaster-client.exe"
ls -lh "$REPO_ROOT/dist/remotemaster-client.exe"
