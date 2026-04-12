# Daemon v2 Production Issues — Handoff Document

## Context

We rebuilt the GSD Cloud daemon as a production-grade background service (Plans 1-5 in `docs/superpowers/plans/`). The code compiles, all tests pass across all 3 repos (daemon, relay, protocol-go). But deploying to production revealed cascading issues.

## Current State (as of 2026-04-11 ~7:20 PM MDT)

- **Daemon:** Running as launchd service, v0.1.0-dev (built from source). Connected to relay.
- **Relay:** Deployed on Fly.io, v23 (latest code with ping loop removed + ReadTimeout=0).
- **Protocol-go:** v0.5.0 published, both repos reference it.
- **DB:** `delivery_failed` enum applied to production. Stale tasks cleaned up.
- **Machine:** Re-paired as `f0c696ff-a601-4e02-99f0-af0302812018`. Shows green on machines page.

## Problem 1: WebSocket connections die after ~5 seconds

### Symptoms
- Relay logs show: `daemon connected` → 5 seconds → `daemon disconnected` with `"failed to read frame header: EOF"`
- Daemon logs show no error — it silently reconnects via the `client.Run()` loop
- Pattern is extremely consistent: exactly 4-5 seconds between connect and disconnect

### Root Cause Investigation

We identified THREE potential causes, fixed two, but the issue persisted with each fix:

**Fix 1: Removed daemon-side `pingManager`**
- `coder/websocket`'s `conn.Ping()` waits for pong by reading, which we assumed raced with `readPump`'s `conn.Read()`
- Removed `pingManager` from `internal/relay/pumps.go`
- Removed `go pingManager(...)` call from `internal/relay/conn.go` `RunOnce()`
- **Result:** Still disconnected after 5 seconds

**Fix 2: Removed relay-side `startPingLoop`**
- Same race condition theory on the relay side
- Removed `startPingLoop()` function from `apps/relay/internal/server/server.go`
- Removed both call sites (daemon handler and browser handler)
- **Result:** Still disconnected after 5 seconds

**Fix 3: Disabled `ReadTimeout` on relay's HTTP server**
- Go's `http.Server.ReadTimeout` applies to the full connection lifecycle including after WebSocket upgrade
- Was set to 15 seconds — would kill idle WebSocket connections
- Changed `ReadTimeout: 15 * time.Second` → `ReadTimeout: 0` in `apps/relay/main.go`
- **Result:** Still disconnected after 5 seconds

### CRITICAL DISCOVERY (late in session)

While investigating, I found that `coder/websocket`'s `Ping()` does NOT actually race with `Read()`. Looking at the source:

```go
func (c *Conn) ping(ctx context.Context, p string) error {
    pong := make(chan struct{}, 1)
    c.activePingsMu.Lock()
    c.activePings[p] = pong
    c.activePingsMu.Unlock()
    
    err := c.writeControl(ctx, opPing, []byte(p))  // WRITE only
    // ...
    select {
    case <-pong:  // Signaled by the Read loop when pong arrives
        return nil
    }
}
```

`Ping()` writes a ping frame via `writeControl` and then waits for a channel signal. The pong is handled by the existing `Read()` loop internally. **There is no concurrent read.** Our entire diagnosis was wrong.

### What actually causes the 5-second disconnect?

**Unknown.** The three fixes we applied (ping removal, ReadTimeout=0) did NOT fix the core issue. What we observed:
- During debugging, the connection sometimes stayed alive for minutes (when the relay had just been deployed and connections were fresh)
- The 5-second disconnects correlated with relay redeployments — each deploy kills existing WebSocket connections, and the daemon reconnects to the new machine, which then dies after 5 seconds for unknown reasons
- The daemon's `gsd-cloud status` always says "connected" because `RelayConnected` is **hardcoded to `true`** (line 168 of `internal/loop/daemon.go`) — TODO was left in the code to fix this

### Areas to explore

1. **Fly.io proxy idle timeout** — Fly's HTTP proxy may have a WebSocket idle timeout that kills connections without traffic. The old daemon had frequent pings keeping it alive. Test by sending a message from daemon to relay within 3 seconds of connecting.

2. **Restore `startPingLoop` on the relay** — Since `Ping()` is actually safe with `Read()`, the original ping loop should work. The 5-second disconnect might have been caused by something else entirely (relay redeployments during testing). Restore the original `startPingLoop` and test with a STABLE relay (no redeployments during the test).

3. **Add connection state tracking** — The daemon's status endpoint lies. `RelayConnected` must reflect actual connection state. Add a callback from the relay client to the daemon when connection state changes.

4. **Add logging to `forwardToDaemonByMachineForUser`** — This function returns `nil` silently when the daemon isn't found. Add a warn log so we can see when messages are dropped.

## Problem 2: BrowseDir not working

### Symptoms
- "Select a Directory" modal shows spinner forever
- Browser console error: `"WebSocket is not connected"` in `sendBrowseDir`
- No BrowseDir messages appear in daemon logs

### Root Cause

The browser opens a `browse-*` WebSocket channel to the relay. It calls `client.connect()` (async), then immediately tries to `sendBrowseDir()`. If the WebSocket isn't open yet, or if it closes before the message is sent, the error fires.

This is likely caused by Problem 1 — the relay-side WebSocket closes after 5 seconds, so the browse channel dies before the BrowseDir message is sent. Or:

The removal of `startPingLoop` from the browser handler means browser WebSocket connections have no keep-alive. The Fly.io proxy or the relay itself may close idle browser connections.

### Flow
```
Browser → opens browse-* WebSocket → relay handleBrowser() → read loop
Browser → sends {"type":"browseDir", "machineId":"...", "path":"/"} 
Relay → OnBrowserMessage → forwardToDaemonByMachineForUser(userID, machineID, msg)
Relay → DaemonPool.GetByMachineForUser(machineID, userID) → finds daemon → sends msg
Daemon → handleBrowse() → fs.BrowseDir(path) → sends browseDirResult back
Relay → OnDaemonMessage → forwardToBrowser(channelID, result)
Browser → receives browseDirResult → renders entries
```

### Areas to explore

1. **Restore `startPingLoop` for browser connections** — Browser WebSocket connections need keep-alive. The ping loop was removed unnecessarily (it was safe all along).

2. **Check if the browse WebSocket connects before sendBrowseDir is called** — The `new-project-modal.tsx` does `client.connect()` then `setRelayClient(client)`. The `FileBrowser` component mounts and calls `browseDir()` on mount. There may be a race where the WebSocket hasn't opened when `sendBrowseDir` fires.

3. **Add logging to relay for BrowseDir forwarding** — `forwardToDaemonByMachineForUser` returns nil silently. Add warn logging when the daemon isn't found so we can see if the message arrives but can't be delivered.

## Problem 3: `gsd-cloud status` lies

### Symptom
`gsd-cloud status` always shows `connected` even when the daemon is disconnected.

### Root Cause
In `internal/loop/daemon.go` line 168:
```go
RelayConnected: true,  // HARDCODED
```

The TODO at line 157 says: `// TODO: report "disconnected" when relay.Client exposes connection state.`

### Fix
Add a `Connected() bool` method to `relay.Client` that tracks connection state via an `atomic.Bool`. Set true after successful `Connect()`, set false when `RunOnce()` returns an error. Wire into `Daemon.Status()`.

## Problem 4: Heartbeat not being sent (FIXED)

### What happened
The v2 refactor replaced `runHeartbeat()` (which sent `protocol.Heartbeat` to the relay every 30s) with `runIdleHeartbeat()` (which only logged locally). The relay uses `last_heartbeat_at` to determine online status. Without heartbeats, the machine shows as offline after 60 seconds.

### Fix applied
Replaced `runIdleHeartbeat` with `runHeartbeat` that sends `protocol.Heartbeat` via `d.client.Send(ctx, msg)` every 30 seconds. This is working — heartbeat timestamps are fresh in the DB.

## Problem 5: `delivery_failed` enum missing (FIXED)

### What happened
Plan 3 added `delivery_failed` to the task status enum in the Drizzle schema and generated migration `0012_fuzzy_loa.sql`. But the migration was never applied to the production Supabase database. Every attempt to mark a task as `delivery_failed` hit `ERROR: invalid input value for enum task_status`.

### Fix applied
Applied the ALTER TYPE directly via psql on the Fly relay container:
```sql
ALTER TYPE task_status ADD VALUE IF NOT EXISTS 'delivery_failed';
```

## Problem 6: Stale tasks flooding poller (FIXED)

### What happened
148 tasks in `pending` or `running` status from before the v2 migration. The poller re-dispatched 100 of them every 30 seconds, flooding the daemon with tasks on every brief connection window.

### Fix applied
```sql
UPDATE tasks SET status = 'failed', completed_at = now(), 
  error = 'cleaned up: stale from daemon v2 migration' 
WHERE status IN ('pending', 'running') 
  AND created_at < now() - interval '5 minutes';
```

## Problem 7: launchd PATH missing (FIXED)

### What happened
launchd runs services with minimal PATH (`/usr/bin:/bin:/usr/sbin:/sbin`). The `claude` binary lives at `~/.local/bin/claude` which isn't in that PATH. Tasks would start but `claude` binary not found.

### Fix applied
`internal/service/launchd.go` `generatePlist()` now captures the user's PATH at install time and embeds it in the plist via `EnvironmentVariables/PATH`.

## Problem 8: `gsd-cloud update` asset name mismatch (FIXED)

### What happened
Release assets are named `gsd-cloud-v0.2.1-darwin-arm64` (with version) but `AssetName()` returned `gsd-cloud-darwin-arm64` (without version).

### Fix applied
`AssetName(version string)` now takes a version parameter. `Download()` extracts the version from the release tag.

## Files Changed Across All Repos

### gsd-build-daemon
- `internal/relay/conn.go` — new connection manager with reconnect loop
- `internal/relay/pumps.go` — read pump, write pump (ping manager removed)
- `internal/relay/send.go` — context-aware Send API
- `internal/relay/wake.go` — sleep/wake detection
- `internal/loop/daemon.go` — heartbeat fix, graceful shutdown, socket server
- `internal/service/launchd.go` — PATH in plist
- `internal/update/update.go` — asset name fix
- `cmd/install.go`, `cmd/uninstall.go`, `cmd/stop.go`, etc. — new CLI commands
- `internal/sockapi/` — Unix socket status API (new package)
- `internal/pidfile/` — PID file management (new package)
- `internal/logging/` — structured logging (new package)
- `internal/service/` — launchd/systemd service management (new package)
- `internal/update/` — self-update mechanism (new package)

### gsd-build-cloud-app/apps/relay
- `internal/server/server.go` — startPingLoop removed, machineStatus push, Hello reconciliation
- `internal/router/router.go` — reconcileTasks, ReconcileOnHello, fail-fast dispatch
- `internal/db/tasks.go` — MarkDeliveryFailed, ListRunningByMachine
- `internal/pools/browsers.go` — ListByUser for machine status broadcasting
- `main.go` — ReadTimeout set to 0, LatestDaemonVersion config

### gsd-build-protocol-go
- `messages.go` — ActiveTasks on Hello, LatestDaemonVersion on Welcome, MachineStatus, UpdateAvailable, TaskCancelled types
- `envelope.go` — new type registrations

## Recommended Next Steps (Priority Order)

1. **Restore `startPingLoop` on relay** — Our diagnosis that `Ping()` races with `Read()` was WRONG. `Ping()` only writes and waits for a channel signal. Restore it and test with a stable relay (no redeployments during test). This likely fixes both the daemon disconnect and the browser browse issue.

2. **Fix `RelayConnected` hardcoding** — Add `Connected() bool` to relay client, wire into daemon status.

3. **Add warn logging to `forwardToDaemonByMachineForUser`** — Silent nil return makes debugging impossible.

4. **Test end-to-end after relay is stable** — Send a task, browse directories, verify the full flow works.

5. **Tag daemon v0.2.3** — Once everything works, tag and release so `curl | sh` installs the fixed version.

6. **Consider adding relay-side WebSocket write-based keep-alive** — Even if Ping() is safe, periodically writing a small control message prevents proxy idle timeouts without relying on WebSocket-level pings.
