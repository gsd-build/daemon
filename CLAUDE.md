# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

GSD Daemon — a local Go binary that connects user machines to the GSD cloud relay via WebSocket. It proxies tasks from the cloud to a local Claude Code process and streams results back. Installed on user machines via `curl -fsSL https://install.gsd.build | sh`.

## Structure

```
main.go                 # Entry point → cmd.Execute() (Cobra CLI)
cmd/                    # CLI commands (start, stop, login, status, version, logs, update, rollback, etc.)
cmd/fake-claude/        # Fake Claude Code binary for e2e tests
internal/
├── relay/              # WebSocket connection to cloud relay
├── session/            # Session state management
├── loop/               # Main event loop (relay ↔ claude)
├── claude/             # Claude Code subprocess management
├── config/             # Config loading (~/.gsd-cloud/)
├── logging/            # slog with mode switching (foreground=human-readable, service=JSON)
├── service/            # Platform-agnostic service manager (macOS/Linux/Windows)
├── api/                # HTTP API
├── sockapi/            # Unix socket API
├── fs/                 # Filesystem operations
├── pidfile/            # PID file management
├── display/            # Terminal display
├── update/             # Binary self-update with rollback safety
tests/e2e/              # Integration tests (stub relay + fake-claude subprocess)
scripts/                # install.sh (user-facing installer), install.test.sh
```

## Commands

```bash
go build -o gsd-cloud .       # Build binary
go test ./...                  # All unit tests
go test ./internal/relay       # Single package
go test ./internal/relay -run TestName   # Single test
go test -short ./...           # Skip integration tests
```

## Key Dependencies

- `github.com/gsd-build/protocol-go` v0.4.0 — shared WebSocket message types (also used by cloud relay)
- `github.com/coder/websocket` — WebSocket client
- `github.com/spf13/cobra` — CLI framework
- `gopkg.in/natefinsh/lumberjack.v2` — log rotation

## Architecture Notes

**Two execution modes.** Direct CLI invocation for user commands (`gsd-cloud start`, `gsd-cloud login`) vs. `--service` flag for systemd/launchd background operation.

**Update safety.** The updater writes a boot marker before swapping binaries. If the new binary crashes within 30s, the previous version is restored via `gsd-cloud rollback`.

**E2E test architecture.** Tests spin up a real daemon process + stub relay server + fake-claude subprocess (built from `cmd/fake-claude/main.go`). This tests the full message flow without hitting real cloud services.

**Signal handling.** Graceful shutdown via context cancellation on SIGINT/SIGTERM.

## Conventions

- This is a **public repo**. Do not add cloud-proprietary code.
- Protocol changes require coordination: edit `gsd-build-protocol-go` → tag new version → bump `go.mod` here and in `gsd-build-cloud-app/apps/relay`.
- User state lives in `~/.gsd-cloud/` (config, login tokens, logs).
