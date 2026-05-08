#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT/server"

echo "Building remotemaster server..."
go build -ldflags="-s -w" -o "$REPO_ROOT/dist/remotemaster-server" .
echo "Done: dist/remotemaster-server"
