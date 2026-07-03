# Contributing to RemoteMaster

Thanks for your interest in improving RemoteMaster. This document covers the
practical bits: repository layout, how to build and test, and what a good pull
request looks like.

## Repository layout

The repo is a Go workspace (`go.work`) with two modules:

| Path      | Module                                        | What it is                                    |
|-----------|-----------------------------------------------|-----------------------------------------------|
| `server/` | `github.com/sirixau/remotemaster/server`      | Relay server + embedded agent (browser) UI    |
| `client/` | `github.com/sirixau/remotemaster/client`      | Windows client (screen capture + input)       |

Key packages:

- `server/session` — session store and code lifecycle
- `server/relay` — the WebSocket bridge between client and agent
- `server/agent/` — static HTML/JS served to the agent (viewer + control)
- `client/relay` — client-side connection, capture loop, wire protocol
- `client/capture`, `client/input`, `client/ui` — Windows-specific capture,
  input injection, and the native status window

The wire protocol is documented in [`docs/protocol.md`](docs/protocol.md).
Keep it in sync with any protocol change.

## Building and testing

Requires Go (see `go.work` for the version).

```sh
# Run all tests (each module is tested separately)
cd server && go test ./...
cd client && go test ./...

# Run the server locally
cd server && go run .

# Cross-compile the Windows client from Linux (needs mingw-w64 for cgo/WebP)
cd client
CC=x86_64-w64-mingw32-gcc CGO_ENABLED=1 GOOS=windows GOARCH=amd64 go build .
```

CI runs `go vet` and `go test` for both modules on every push and pull
request (`.github/workflows/ci.yml`), and the release workflow will not build
binaries unless tests pass.

## Pull requests

- Keep PRs focused: one feature or fix per PR where practical.
- Add or update tests for anything with logic in it — the session store,
  protocol encode/decode, and HTTP handlers all have existing test files to
  extend.
- Update `docs/` when you change the protocol, deployment story, or security
  posture, and `README.md` if user-facing behavior changes.
- Run `gofmt` (or `go fmt ./...`) before committing.
- Security-sensitive changes (auth, origin checks, rate limiting, anything in
  `docs/security.md`) get extra scrutiny — explain your reasoning in the PR
  description.

## Reporting security issues

Please do not open public issues for vulnerabilities — see
[`SECURITY.md`](SECURITY.md).
