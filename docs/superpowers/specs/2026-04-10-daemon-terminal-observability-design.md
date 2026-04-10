# Daemon Terminal Observability — Design Spec

## Overview

Make `gsd-cloud start` show rich, color-coded output of everything Claude is doing. Two visual tiers using color weight: Claude's words at full brightness, everything else dim. Three verbosity levels via CLI flags. Connection lifecycle and idle heartbeat provide ambient awareness.

## Visual Design

Left-aligned, no indentation. Visual hierarchy through brightness only:

- **Claude's text** — full brightness white. The primary content.
- **Tool calls** — dim, prefixed with `→`. Background context.
- **Thinking** — dim. No symbol. Truncated to 80 chars in default mode.
- **System/result** — dim for metadata, bold for summaries.

Single symbol vocabulary: `→` for tool calls. Everything else is communicated through brightness.

### Default Output

```
──────────────────────────────────────────────
Fix the login bug in auth.ts
cwd: ~/Developer/myproject
model: claude-sonnet-4-5-20250514
──────────────────────────────────────────────
Let me look at the auth module...
→ Read src/auth.ts  84 lines
→ Grep "validateToken"  3 files

I can see the issue. The token validation
is checking expiry with < instead of <=.

→ Edit src/auth.ts  ok
→ Bash npm test  ok

Fixed. Tests pass.

══════════════════════════════════════════════
Done | 5 turns | 12.3s | $0.0312
══════════════════════════════════════════════
```

### Debug Output (`--debug`)

Full firehose. Tool call inputs shown as key: value pairs. Tool results shown with line count and content (capped at 30 lines). Thinking shown in full. Session ID in result summary.

```
──────────────────────────────────────────────
Fix the login bug in auth.ts
cwd: ~/Developer/myproject
model: claude-sonnet-4-5-20250514
──────────────────────────────────────────────
Session started | model: claude-sonnet-4-5-20250514 | 42 tools
Let me look at the auth module to understand the token validation
logic. The user mentioned a login bug, so I should check how tokens
are validated on login and look for comparison issues, off-by-one
errors, or timezone problems in the expiry check.
→ Read src/auth.ts
  file_path: src/auth.ts
  result (84 lines)
  import { verify } from 'jsonwebtoken';
  import { TokenPayload } from './types';
  ...
  ... (54 more lines)
→ Grep "validateToken"
  pattern: validateToken
  result (3 lines)
  src/auth.ts:47:  return decoded.exp < Date.now() / 1000;
  src/middleware/auth-guard.ts:12:  if (!validateToken(req.headers.authorization)) {
  src/routes/protected.ts:8:import { validateToken } from '../auth';

I can see the issue. The token validation in auth.ts line 47 is
checking expiry with < instead of <=.

→ Edit src/auth.ts
  file_path: src/auth.ts
  old_string: return decoded.exp < Date.now() / 1000;
  new_string: return decoded.exp <= Date.now() / 1000;
  result (1 lines)
  ok
→ Bash npm test
  command: npm test
  result (12 lines)
  PASS src/__tests__/auth.test.ts
  PASS src/__tests__/upload.test.ts
  Tests: 24 passed, 24 total
  ...

Fixed. Tests pass.

══════════════════════════════════════════════
Done | 5 turns | 12.3s | $0.0312
session: sess_abc123def456
══════════════════════════════════════════════
```

### Quiet Output (`--quiet`)

Connection status and heartbeat only. No event output.

```
Connecting to wss://relay.gsd.build/ws/daemon as 913d0400...
relay connected
16:03 ♥ connected · idle
16:08 ♥ connected · idle
relay disconnected (2h17m) — network timeout
reconnecting in 1s...
relay connected
16:45 ♥ connected · idle
```

### Permission Request Flow (Default + Debug only)

```
→ Bash rm -rf /tmp/build-cache
⚠ PERMISSION REQUEST: Bash
command: rm -rf /tmp/build-cache
waiting for approval...

Approved — resuming.

→ Bash rm -rf /tmp/build-cache  ok
```

### Question Flow (Default + Debug only)

```
? Which database should I use for the migration?
  1) PostgreSQL
  2) SQLite
  3) MySQL
waiting for answer...

Answer: PostgreSQL
```

### Error Case

```
──────────────────────────────────────────────
Deploy to production
cwd: ~/Developer/myproject
model: claude-sonnet-4-5-20250514
──────────────────────────────────────────────
→ Bash vercel deploy --prod  ERROR: Missing VERCEL_TOKEN

FAILED  claude exited with code 1: Error: deployment requires authentication

16:20 ♥ connected · idle
```

## Event Formatting Rules

| Event | Default | Debug |
|-------|---------|-------|
| **Thinking** | Dim, truncated to 80 chars, one line | Dim, full text, multi-line |
| **Tool call** | Dim, `→ <Name> <display_path>  <result_summary>` | Same + indented key: value inputs |
| **Tool result** | Merged into tool call line | Indented content, capped at 30 lines |
| **Claude text** | Full brightness, streams progressively | Same |
| **System init** | Hidden | Dim, `Session started \| model \| N tools` |
| **Result** | `Done \| N turns \| Xs \| $X.XXXX` with `═══` borders | Same + session ID |
| **Request banner** | `───` rule, prompt text, cwd, model (dim) | Same |
| **Complete** | No banner, just the result summary block | Same |
| **Error** | `FAILED` + error message | Same |

### Tool Call Display Path

| Tool | Display |
|------|---------|
| Read, Write, Edit, Glob | `file_path` value |
| Bash | `command` value (truncated to 60 chars) |
| Grep | `pattern` value (quoted) |
| Other | First string value from input, or empty |

### Tool Result Summary (appended to tool call line)

- Empty or "ok": `ok`
- Multi-line: `N lines`
- Short single line (< 60 chars): the content itself
- Error: `ERROR: <message>` (red, full brightness)

## Streaming Deltas

Claude emits `stream_event` JSON lines with `content_block_start`, `content_block_delta`, and `content_block_stop`. Processing:

1. **`content_block_start` (type: text):** Print `\n` (newline before Claude text). Set streaming flag.
2. **`content_block_delta` (text_delta):** Print delta text directly to stdout with `fmt.Print()`. Text appears progressively.
3. **`content_block_stop`:** Print `\n` to close the block. Set skip flag so the subsequent `assistant` event doesn't duplicate the text.

In `--quiet` mode: deltas are not printed.

## Idle Heartbeat

Every 5 minutes when no task is running: `HH:MM ♥ connected · idle`

Prints in all verbosity modes. Pauses during active tasks. The heartbeat goroutine lives in the daemon loop, not the actor.

## Connection Lifecycle

| Event | Output |
|-------|--------|
| Initial connect | `Connecting to <url> as <machine_id>...` then `relay connected` |
| Disconnect | `relay disconnected (<duration>) — <reason>` |
| Reconnecting | `reconnecting in <delay>...` |
| Reconnected | `relay connected` |
| Token expired | `machine token has expired — run gsd-cloud login to re-pair` |

Backoff resets to 1s if the previous connection lasted > 2 minutes.

## CLI Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--quiet` | bool | false | Connection status and heartbeat only |
| `--debug` | bool | false | Full event details (thinking, tool results, inputs) |

`--quiet` and `--debug` are mutually exclusive. `--debug` wins if both set.

## Architecture

### New package: `internal/display`

| File | Responsibility |
|------|----------------|
| `display.go` | `VerbosityLevel` enum, ANSI constants, HR/Truncate helpers, request/complete/error banners |
| `format.go` | `FormatEvent(raw, level)` — dispatches by event type, returns formatted string. `FormatEventSkipText` variant for post-streaming dedup. |
| `stream.go` | `StreamHandler` — processes stream_event deltas for progressive text rendering, tracks skip state |

Ported from the spike at `gsd-cloud/apps/daemon/internal/display/` with modifications:
- Remove all indentation (left-align everything)
- Replace `▶` with `→` for tool calls
- Replace `◆ thinking:` with plain dim text for thinking
- Remove `◀` result prefix in debug mode
- Remove task label from banners (prompt text only)
- Remove `TASK COMPLETE` banner (result summary block is sufficient)

### Modified files

| File | Changes |
|------|---------|
| `session/actor.go` | Add `verbosity` and `stream *display.StreamHandler` fields. In `handleEvent`: route stream_events to StreamHandler, format and print other events. In `SendTask`: print request banner. In `handleResult`: print result summary or error. |
| `session/manager.go` | Accept `VerbosityLevel` in constructor, pass to actors on creation. |
| `loop/daemon.go` | Accept `VerbosityLevel` in constructor, pass to manager. Color-format connection lifecycle messages. Add idle heartbeat goroutine (5min ticker, paused during tasks). |
| `cmd/start.go` | Add `--quiet` and `--debug` flags. Resolve to `VerbosityLevel`. Pass to `loop.New()`. |

### Data flow

```
claude stdout (pty)
  ↓
parser.go: Parse() → Event{Type, Raw}
  ↓
actor.go: handleEvent(event)
  ├─ stream_event? → StreamHandler.Handle(raw) → prints delta to stdout
  ├─ other event? → display.FormatEvent(raw, level) → prints to stdout
  ├─ write to WAL
  └─ send to relay
```

Display happens before WAL/relay so the user sees events as fast as possible. Display errors (if any) are swallowed — they must never interrupt the relay pipeline.

## Out of Scope

- Log file output (stdout only for now)
- Custom color themes
- Terminal width detection for HR sizing
- Mouse/TUI interactivity
