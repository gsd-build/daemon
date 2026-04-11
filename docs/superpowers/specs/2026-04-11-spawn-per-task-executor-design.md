# Spawn-Per-Task Executor — Design Spec

## Overview

Replace the long-lived `claude -p --input-format stream-json` process with a spawn-per-task model: each incoming task spawns `claude -p --resume <session_id> "prompt"`, the process runs to completion, emits stream-json events, and exits. No processes remain alive between tasks.

## Why

The current executor keeps a Claude CLI process (Node.js) alive indefinitely per session. Each idle process consumes ~50-80MB of memory doing nothing. Users with multiple browser sessions accumulate orphaned processes. The long-lived model also requires complex process lifecycle management: PTY hacks for stdout buffering, stdin pipe coordination, a `ready` channel to synchronize message delivery, and a `RestartWithGrant` dance to handle permission approvals.

Spawn-per-task eliminates all of this. The session state lives in Claude's session store on disk, referenced by session ID. Each spawn picks up where the last one left off via `--resume`.

## Command Format

```
claude -p \
  --resume <claude_session_id> \
  --output-format stream-json \
  --verbose \
  --include-partial-messages \
  --model <model> \
  --effort <effort> \
  --permission-mode <mode> \
  --append-system-prompt <prompt> \
  --allowedTools <tool1> --allowedTools <tool2> \
  "the user's prompt text"
```

For the first task in a session (no claude session ID yet), omit `--resume`. The result event returns a `session_id` that all subsequent spawns use.

The prompt is passed as a positional CLI argument, not via stdin. Stdin is not used.

## Stdout

Read stdout from a regular pipe (no PTY). Claude CLI buffers the first ~4KB of output, then flushes per-line. This means the first few events (system init, first delta or two) arrive in a batch, then streaming works normally. The delay is imperceptible to users since it's a fraction of a second against Claude's multi-second response time.

Stderr is captured in a ring buffer for error reporting, same as today.

## What Changes

### `internal/claude/executor.go`

The executor becomes a single-shot runner. The new interface:

```go
type Options struct {
    BinaryPath       string
    CWD              string
    Model            string
    Effort           string
    PermissionMode   string
    SystemPrompt     string
    ResumeSession    string   // empty = new session
    AllowedTools     []string
    Env              []string
    Prompt           string   // the user's message
}

// Run spawns claude, parses events until the process exits, returns.
// This is the only method. No Send, no Close, no ready channel.
func (e *Executor) Run(ctx context.Context, onEvent func(Event) error) error
```

**Removed:**
- `Send()` — prompt is a CLI argument, no stdin
- `Close()` — process exits on its own, context cancellation kills it
- `ready` channel — no synchronization needed
- `stdin` pipe — not used
- `shuttingDown` atomic — context cancellation handles this
- `started` flag — Run is called once and returns
- PTY allocation (`pty_unix.go`, `openClaudePTY`, `ptySysProcAttr`) — stdout is a regular pipe

**Kept:**
- Stderr ring buffer (captures crash/error output)
- `Parse()` function (reads NDJSON from stdout)
- `Event` type (unchanged)

### `internal/claude/pty_unix.go`

Delete this file entirely. No PTY needed.

### `internal/session/actor.go`

The actor no longer holds a long-lived executor. Instead:

- `Run()` becomes an idle loop that waits for tasks via a channel
- When a task arrives, it spawns an executor with the prompt, waits for it to finish
- The `claudeSessionID` is passed to each spawn via `--resume`
- On permission denial: the actor stores the pending denial, waits for a response, then spawns a new executor with `--allowedTools` updated and the original prompt. No `RestartWithGrant` — just a regular spawn.

**Removed:**
- `RestartWithGrant()` — replaced by a regular spawn with updated tools
- `snapshotExecutor()` / executor mutex — no long-lived executor to protect
- `runDone` channel — no executor goroutine lifecycle to manage

**New:**
- `taskCh chan protocol.Task` — actor receives tasks via channel instead of direct `SendTask` call racing with executor lifecycle
- Task processing loop in `Run()` that spawns executors on demand

### `internal/session/manager.go`

Minimal changes. The manager still creates actors and runs them. The Spawn goroutine pattern stays the same.

### `internal/loop/daemon.go`

No changes to the daemon loop. It still calls `actor.SendTask()` which now sends to the task channel.

### `cmd/start.go`

No changes.

### Display package (`internal/display/`)

No changes. The display package works on raw event bytes regardless of how the executor is spawned.

## Process Lifecycle

### First task in a new session

```
1. Web UI sends Task (no ClaudeSessionID)
2. Actor receives task, spawns:
   claude -p --output-format stream-json --verbose --include-partial-messages \
     --model <m> "prompt"
3. Events stream → display + WAL + relay
4. Result event contains session_id → actor stores it
5. Process exits
6. Actor sends TaskComplete to relay
```

### Subsequent tasks (resume)

```
1. Web UI sends Task (with ClaudeSessionID from previous result)
2. Actor receives task, spawns:
   claude -p --resume <session_id> --output-format stream-json ... "prompt"
3. Events stream → display + WAL + relay
4. Process exits
5. Actor sends TaskComplete to relay
```

### Permission denial flow

```
1. Claude hits a tool that needs permission
2. Result event has permission_denials array
3. Actor sends PermissionRequest to relay, stores pending denial
4. User approves in web UI → relay sends PermissionResponse
5. Actor spawns new executor with:
   --resume <session_id> --allowedTools <granted_tool> "original prompt"
6. Claude resumes, uses the tool, completes
7. Actor sends TaskComplete
```

### Task cancellation / daemon shutdown

Context cancellation kills the subprocess via `exec.CommandContext`. The process receives SIGKILL. No cleanup needed — the next spawn just `--resume`s from the last committed state.

## What Stays the Same

- WAL (write-ahead log) — still records every event
- Relay forwarding — unchanged
- Display output — unchanged (just shipped in v0.1.8)
- Connection lifecycle — unchanged
- Heartbeat — unchanged
- CLI flags (--quiet, --debug) — unchanged
- Session ID tracking on web UI side — unchanged

## Testing

Existing actor tests use `fake-claude`, a test binary that emits canned events and exits. This already matches the spawn-per-task model (fake-claude runs and exits). The tests should require minimal changes — mainly updating how the actor is driven (channel-based instead of direct executor lifecycle).

The e2e test spawns a full daemon with fake-claude and sends tasks through a real WebSocket relay. This test validates the full pipeline and should pass with the new executor after updating the fake-claude invocation pattern.

## Migration

This is a breaking internal change but the external interfaces are identical:
- Web UI sends the same Task messages
- Relay protocol is unchanged
- CLI flags are unchanged
- WAL format is unchanged

Users update by downloading v0.1.9 (or whatever the next version is). No migration steps.

## Out of Scope

- Multi-turn within a single task (each task is one prompt → one response)
- Session idle timeout / cleanup (sessions are just files on disk now, clean up separately)
- Removing `--verbose` or `--include-partial-messages` flags
