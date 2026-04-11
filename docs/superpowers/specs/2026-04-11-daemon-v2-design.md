# Daemon v2 Design Spec

## Goal

Rebuild the GSD Cloud daemon as a production-grade background service with a bulletproof WebSocket connection layer, proper lifecycle management, and transparent failure handling. The daemon runs invisibly on the user's machine, auto-starts on boot, auto-restarts on crash, and handles any number of concurrent tasks up to the machine's capacity.

## Architecture

The daemon is a single Go binary that runs as a launchd agent (macOS) or systemd user service (Linux). It maintains a persistent WebSocket connection to the cloud relay, receives tasks, spawns Claude CLI processes, and streams results back. A Unix domain socket exposes a local status API for the CLI.

```
launchd/systemd
  └─ gsd-cloud (daemon process)
       ├─ Connection Manager
       │    ├─ Read Pump (goroutine)
       │    ├─ Write Pump (goroutine)
       │    └─ Ping Manager (goroutine)
       ├─ Session Manager
       │    ├─ Actor [session-1] → claude subprocess
       │    ├─ Actor [session-2] → claude subprocess
       │    └─ Actor reaper (goroutine)
       ├─ Unix Socket Server
       │    └─ /status, /health, /sessions
       └─ Lifecycle Manager
            ├─ Signal handler
            ├─ Sleep/wake detector
            └─ Version checker
```

## Repos Touched

- `gsd-build-daemon` — connection layer rewrite, service installer, Unix socket, CLI commands
- `gsd-build-cloud-app/apps/relay` — fail-fast dispatch, reconnect reconciliation, version in Welcome, machine status push
- `gsd-build-protocol-go` — new fields on Hello/Welcome, new message types (machineStatus, updateAvailable)

---

## 1. Connection Layer

### Read Pump

A dedicated goroutine that owns all reads from the WebSocket. Calls `conn.Read()` in a tight loop, parses each frame into a `protocol.Envelope`, and dispatches to the registered message handler. If `conn.Read()` returns an error, the read pump signals the connection manager to reconnect and exits.

The read pump does not hold any locks and does not touch the write path.

### Write Pump

A dedicated goroutine that owns all writes to the WebSocket. Drains a single buffered channel (`sendCh`, capacity 512) and calls `conn.Write()` for each message. If `conn.Write()` returns an error, the write pump signals the connection manager to reconnect and exits.

The write pump is the only goroutine that writes to the connection. All other code enqueues messages onto `sendCh`.

### Ping Manager

A goroutine that enqueues a WebSocket ping via the write pump every 25 seconds. Tracks consecutive failures. After 3 failed pings (no pong within 5 seconds each), signals the connection manager to reconnect.

### Send API

```go
func (c *Client) Send(ctx context.Context, msg any) error
```

Marshals the message to JSON and attempts to enqueue onto `sendCh`. Uses a `select` with the caller's context:

- If the channel has space, enqueues immediately and returns nil.
- If the channel is full, blocks until space is available or the context expires.
- If the context expires (timeout or cancellation), returns an error.

Callers pass a context with a 5-second timeout for stream events. Control messages (TaskComplete, TaskError, TaskCancelled, TaskStarted) use a 30-second timeout because losing these is more costly. The caller handles the error — for stream events, the actor logs and continues (one dropped frame is acceptable; the browser will show a gap). For control messages, the actor retries once after a short delay.

The `Send` API does not block indefinitely. No goroutine can hang on a network partition.

### Connection Manager

Coordinates the lifecycle of read pump, write pump, and ping manager. Responsibilities:

- **Initial connect:** Dial the relay with a 15-second timeout. Send Hello. Wait for Welcome (10-second timeout). Spin up pumps.
- **Reconnect:** When any pump signals failure, the connection manager tears down all three goroutines, closes the old connection, and enters the reconnect loop.
- **Backoff:** Jittered exponential backoff starting at 1 second, capping at 60 seconds. Jitter is ±20% to prevent thundering herd if multiple daemons reconnect simultaneously. If the previous connection was healthy for >2 minutes, backoff resets to 1 second.
- **Sleep/wake detection:** On macOS, monitors `IOPMAssertion` notifications (via cgo or polling `/usr/bin/pmset -g assertions`). On Linux, monitors `systemd-logind` inhibitor state. When wake-from-sleep is detected, the connection manager skips backoff and reconnects immediately.
- **Disconnected state:** While disconnected, `Send()` calls will block (up to their context timeout) and then fail. The connection manager does not buffer messages during disconnection — there is no offline queue.

### Read Limit

`conn.SetReadLimit(1 << 20)` (1 MB) on every new connection, matching the relay's limit.

---

## 2. Reconnect State Sync

### Hello Message (daemon → relay)

On every connect (initial or reconnect), the daemon sends a Hello that includes:

```go
type Hello struct {
    Type          string   `json:"type"`
    MachineID     string   `json:"machineId"`
    DaemonVersion string   `json:"daemonVersion"`
    OS            string   `json:"os"`
    Arch          string   `json:"arch"`
    ActiveTasks   []string `json:"activeTasks,omitempty"` // task IDs currently executing
}
```

`ActiveTasks` lists every task ID that the daemon is currently executing or has queued. On a fresh connect with no running tasks, this is empty.

### Welcome Message (relay → daemon)

```go
type Welcome struct {
    Type                string `json:"type"`
    LatestDaemonVersion string `json:"latestDaemonVersion,omitempty"`
}
```

The relay includes the latest published daemon version so the daemon can compare and notify the user if an update is available.

### Relay-Side Reconciliation

When the relay receives a Hello from a reconnecting daemon:

1. **Tasks the relay thinks are running on this machine, but the daemon didn't list in `ActiveTasks`:** The relay marks these tasks as `failed` with reason `"daemon lost task on reconnect"`. The browser shows an error with a retry button.

2. **Tasks the daemon listed in `ActiveTasks` that the relay doesn't have as `running`:** This is a no-op. The daemon keeps executing them. When they complete, the daemon sends TaskComplete as normal. If the relay has already marked them as failed (e.g., due to a previous timeout), the relay ignores the late TaskComplete.

The daemon never kills local work because the relay forgot about it. Local tasks always run to completion unless explicitly cancelled by the user.

---

## 3. Fail-Fast Task Delivery

When the browser sends a task to the relay for dispatch:

1. Relay checks: is the target daemon online (present in the daemon pool)?
2. If **offline:** relay returns an error to the browser immediately. The browser stores the task in the DB with status `delivery_failed` and shows it in the chat with a "Machine offline — retry when connected" message and a retry button.
3. If **online:** relay forwards the task via WebSocket. If the WebSocket write fails (connection dropped between the pool check and the write), the relay returns an error. Same `delivery_failed` handling.

There is no hidden queuing. There is no poller-based retry. The user always knows whether their task was delivered.

### Machine Status Indicator

The relay tracks daemon online/offline state. When a daemon connects or disconnects, the relay pushes a `machineStatus` event to all connected browsers for that user:

```go
type MachineStatus struct {
    Type      string `json:"type"` // "machineStatus"
    MachineID string `json:"machineId"`
    Online    bool   `json:"online"`
}
```

The browser shows a green/red indicator next to the machine name. The user can see at a glance whether their machine is reachable before sending a task.

---

## 4. Session & Actor Architecture

### Actor Per Session

Each session gets one Actor goroutine. The actor:

- Receives tasks on a buffered channel (capacity 1 — one executing, one queued).
- Spawns a `claude` CLI subprocess per task.
- Streams events to the relay via `client.Send(ctx, msg)` with a 5-second timeout.
- Handles permission and question flows (waits for relay response).
- On task completion, sends TaskComplete with a 30-second timeout.
- On cancel (user-initiated via Stop message), kills the subprocess with SIGTERM, waits 5 seconds, then SIGKILL. Sends TaskCancelled.
- Stays alive between tasks for `--resume` session continuity.

If `Send()` fails for a stream event, the actor logs the error and continues. The Claude subprocess keeps running — one dropped frame doesn't warrant killing the task. If `Send()` fails for a control message (TaskComplete, TaskStarted), the actor retries once after 2 seconds. If the retry also fails, the actor logs an error and moves on. The relay will eventually time out the task.

### Concurrency Control

Default maximum concurrent tasks: `runtime.NumCPU()` (e.g., 10 on an M1 Pro). Configurable via `~/.gsd-cloud/config.json` field `maxConcurrentTasks`. A value of 0 means no limit.

The session manager tracks the count of actors with in-flight tasks. When a new task arrives and the count equals the limit, the daemon responds with a `TaskError` containing `"machine at capacity — try again shortly"`. The browser shows this error. No silent queuing.

As a last-resort safety net, the daemon checks system memory on each task spawn. If available memory is below 10% of total, the task is rejected with a `TaskError` regardless of the concurrency limit. This catches pathological cases where individual tasks consume unexpectedly large amounts of memory.

### Per-Task Timeout

Every task has a deadline. Default: 30 minutes. Configurable via `~/.gsd-cloud/config.json` field `taskTimeoutMinutes`.

When the deadline expires:
1. Send SIGTERM to the Claude subprocess.
2. Wait 10 seconds for clean exit.
3. Send SIGKILL if still running.
4. Send `TaskError` with `"task timed out after 30 minutes"`.

### Actor Reaper

A goroutine running on a 5-minute tick. Checks each actor's `lastActiveAt` timestamp. Actors idle for more than 30 minutes are stopped and removed from the session map. This prevents goroutine leaks from abandoned sessions.

### Child Process Lifecycle

On Linux, spawned Claude subprocesses are placed in a process group (`Setpgid: true`). On daemon exit (clean or crash), the process group receives SIGTERM via the OS. On macOS, subprocesses are tracked via `kqueue` process monitoring. As a fallback on both platforms, a PID file is written for each running Claude process to `~/.gsd-cloud/pids/`. On startup, the daemon checks for stale PID files and kills any orphaned processes.

---

## 5. Background Service

### Install Command

`gsd-cloud install` performs:

1. Verifies the binary exists at `~/.gsd-cloud/bin/gsd-cloud`.
2. Verifies the machine is paired (config.json exists with machineID and authToken).
3. Generates the platform-specific service file.
4. Loads and starts the service.

**macOS — launchd agent:**

Writes `~/Library/LaunchAgents/build.gsd.cloud.daemon.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>build.gsd.cloud.daemon</string>
  <key>ProgramArguments</key>
  <array>
    <string>~/.gsd-cloud/bin/gsd-cloud</string>
    <string>start</string>
    <string>--service</string>
  </array>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>~/.gsd-cloud/logs/daemon.log</string>
  <key>StandardErrorPath</key>
  <string>~/.gsd-cloud/logs/daemon.log</string>
  <key>ThrottleInterval</key>
  <integer>10</integer>
</dict>
</plist>
```

Runs `launchctl load ~/Library/LaunchAgents/build.gsd.cloud.daemon.plist`.

**Linux — systemd user service:**

Writes `~/.config/systemd/user/gsd-cloud.service`:

```ini
[Unit]
Description=GSD Cloud Daemon
After=network-online.target

[Service]
Type=simple
ExecStart=%h/.gsd-cloud/bin/gsd-cloud start --service
Restart=always
RestartSec=5
StartLimitBurst=5
StartLimitIntervalSec=300

[Install]
WantedBy=default.target
```

Runs `systemctl --user daemon-reload && systemctl --user enable --now gsd-cloud`.

### Uninstall Command

`gsd-cloud uninstall` stops the service, removes the service file, and reloads the service manager. Does not delete the binary or config — only the service registration.

### Start Command

`gsd-cloud start` behavior:
- Without `--foreground`: checks if the service is installed. If not, runs `install`. If already installed, ensures it's running (`launchctl start` / `systemctl --user start`).
- With `--foreground`: runs the daemon in the current terminal with human-readable log output to stdout. Does not touch the service manager. For debugging.
- With `--service`: internal flag used by the service file. Runs with JSON structured logging to stdout (which the service manager redirects to the log file). Not intended for direct user use.

### Stop, Restart Commands

`gsd-cloud stop` stops the service via `launchctl stop` / `systemctl --user stop`. Does not unregister it — the service will start again on next boot.

`gsd-cloud restart` stops and starts the service.

---

## 6. Graceful Shutdown

On SIGTERM (from service manager or `gsd-cloud stop`):

1. **Stop accepting new tasks.** Incoming Task messages get a TaskError response: `"daemon shutting down"`.
2. **Wait for in-flight tasks.** Each actor with a running Claude subprocess gets up to 30 seconds to complete. The daemon sends a "going offline" heartbeat to the relay immediately, so the browser shows the machine going offline.
3. **Escalate.** After 30 seconds, send SIGTERM to all remaining Claude subprocesses. Wait 5 seconds. Send SIGKILL to any survivors.
4. **Clean close.** Close the WebSocket with StatusGoingAway. Exit 0.

On SIGINT (Ctrl+C in foreground mode): same as SIGTERM.

On SIGKILL or crash: the relay detects the closed WebSocket on the next ping cycle (within 25 seconds). The relay marks the machine as offline and pushes `machineStatus` to browsers. Any tasks the relay has as `running` for this machine remain in `running` state for 60 seconds (grace period), then the relay marks them as `failed` with reason `"daemon disconnected unexpectedly"`.

---

## 7. Logging

### Structured Logging

Uses Go's `log/slog` package with JSON output when running as a service (`--service` flag) and human-readable text output in foreground mode.

Log levels: `debug`, `info`, `warn`, `error`. Default: `info`. Configurable via `~/.gsd-cloud/config.json` field `logLevel`.

### Log Rotation

Log file: `~/.gsd-cloud/logs/daemon.log`. Rotation: 10 MB max file size, keep 3 rotated files (`daemon.log.1`, `daemon.log.2`, `daemon.log.3`). Rotation is handled by the daemon itself using `lumberjack` or equivalent — no dependency on external log rotation tools.

### Logs Command

`gsd-cloud logs` tails the log file with `tail -f`. Shorthand for `tail -f ~/.gsd-cloud/logs/daemon.log`.

---

## 8. Update Mechanism

### Version Notification

The relay includes `latestDaemonVersion` in the Welcome message. The daemon compares this against its compiled-in version string. If newer:

1. Log an info message: `"update available: v0.1.13 (current: v0.1.12)"`.
2. Send an `updateAvailable` event to the relay, which forwards it to connected browsers. The browser shows a non-intrusive banner.

### Update Command

`gsd-cloud update` performs:

1. Query the GitHub Releases API for the latest `daemon/v*` release.
2. Download the binary for the current OS/architecture.
3. Verify the SHA256 checksum.
4. Copy the current binary to `~/.gsd-cloud/bin/gsd-cloud.prev` (for rollback).
5. Replace `~/.gsd-cloud/bin/gsd-cloud` with the new binary.
6. Restart the service.

### Crash-Count Rollback

On startup, the daemon writes its version to `~/.gsd-cloud/boot-marker`. If the daemon process exits within 30 seconds of startup 3 times in a row (tracked by launchd `ThrottleInterval` / systemd `StartLimitBurst`), the service manager stops restarting it.

`gsd-cloud status` detects this state (service registered but not running) and prints: `"Daemon failed to start repeatedly. Run 'gsd-cloud rollback' to restore the previous version."`

`gsd-cloud rollback` copies `gsd-cloud.prev` back to `gsd-cloud`, removes `gsd-cloud.prev`, resets the service manager's failure counter, and restarts the service. If `gsd-cloud.prev` doesn't exist, prints an error.

A `rollback-attempted` flag file prevents infinite rollback loops. If the previous version also crashes 3 times, the service stays stopped and the user is told to reinstall.

---

## 9. Unix Socket & Status API

### Socket

The daemon opens a Unix domain socket at `~/.gsd-cloud/daemon.sock` on startup. Permissions are set to `0600` (owner-only access). The socket is removed on clean shutdown. On startup, if the socket file already exists (stale from a crash), the daemon removes it before binding.

### Endpoints

HTTP/1.1 over the Unix socket. No TLS (local-only, owner-only permissions).

**GET /health**

Returns `200 OK` with body `{"status":"ok"}` if the daemon is running and connected to the relay. Returns `503 Service Unavailable` with body `{"status":"disconnected"}` if the daemon is running but the relay connection is down.

**GET /status**

Returns JSON:
```json
{
  "version": "0.1.13",
  "uptime": "2h34m",
  "relayConnected": true,
  "relayURL": "wss://relay.gsd.build/ws/daemon",
  "machineID": "5c6651b6-...",
  "activeSessions": 3,
  "inFlightTasks": 1,
  "maxConcurrentTasks": 10,
  "logLevel": "info"
}
```

**GET /sessions**

Returns JSON array of active sessions:
```json
[
  {
    "sessionID": "abc-123",
    "state": "executing",
    "taskID": "task-456",
    "startedAt": "2026-04-11T20:00:00Z",
    "idleSince": null
  },
  {
    "sessionID": "def-789",
    "state": "idle",
    "taskID": "",
    "startedAt": null,
    "idleSince": "2026-04-11T19:45:00Z"
  }
]
```

### CLI Integration

`gsd-cloud status` tries the Unix socket first:

```
gsd-cloud v0.1.13
status:     connected
relay:      wss://relay.gsd.build/ws/daemon
uptime:     2h 34m
sessions:   3 active (1 executing, 2 idle)
tasks:      1 in-flight (max 10)
```

If the socket doesn't exist or the daemon isn't running:

```
gsd-cloud v0.1.13
status:     not running
machine:    5c6651b6-...
relay:      wss://relay.gsd.build/ws/daemon

Run 'gsd-cloud start' to connect.
```

---

## 10. CLI Surface

```
gsd-cloud login [code]        Pair machine with GSD Cloud account
gsd-cloud install              Register as background service and start
gsd-cloud uninstall            Stop and remove background service
gsd-cloud start                Start daemon (installs service if needed)
gsd-cloud start --foreground   Run in terminal with live output
gsd-cloud stop                 Stop the background service
gsd-cloud restart              Restart the background service
gsd-cloud status               Show live daemon state
gsd-cloud update               Download and install latest version
gsd-cloud rollback             Restore previous version after failed update
gsd-cloud logs                 Tail the daemon log file
gsd-cloud version              Print version
```

---

## 11. Configuration

`~/.gsd-cloud/config.json` gains new optional fields:

```json
{
  "machineId": "5c6651b6-...",
  "authToken": "...",
  "relayUrl": "wss://relay.gsd.build/ws/daemon",
  "serverUrl": "https://app.gsd.build",
  "tokenExpiresAt": "2027-04-11T00:00:00Z",
  "maxConcurrentTasks": 10,
  "taskTimeoutMinutes": 30,
  "logLevel": "info"
}
```

All new fields have sensible defaults and are optional. Existing config files work without modification.

### Token Refresh

The existing token refresh mechanism (check every 6 hours, refresh if within 7 days of expiry) is retained. On successful refresh, the new token and expiry are written to config.json. If the relay rejects a connection with `token_expired`, the daemon exits with a clear message: `"Machine token expired — run 'gsd-cloud login' to re-pair."` The service manager will try to restart, but the daemon will keep exiting until the user re-authenticates. `gsd-cloud status` detects this state and shows the re-login instruction.

---

## 12. Protocol Changes

### New/Modified Message Types

| Type | Direction | Purpose |
|---|---|---|
| `hello` | daemon → relay | Add `activeTasks []string` field |
| `welcome` | relay → daemon | Add `latestDaemonVersion string` field |
| `machineStatus` | relay → browser | New: `machineId`, `online` |
| `updateAvailable` | daemon → relay → browser | New: `currentVersion`, `latestVersion` |

### New Task Status

Add `delivery_failed` to the task status enum in the DB. Represents a task the user submitted but the relay could not deliver because the daemon was offline.

---

## Scope Breakdown

This spec decomposes into 5 independent subsystems that can be built and tested separately:

1. **Connection layer** — read/write pumps, send API, reconnect coordinator, sleep/wake detection. Daemon repo only.
2. **Service installer** — install/uninstall/start/stop/restart commands, launchd/systemd generation, logging infrastructure. Daemon repo only.
3. **Fail-fast & state sync** — fail-fast dispatch, delivery_failed status, Hello reconciliation, machine status push. Relay + daemon + protocol-go.
4. **Actor hardening** — concurrency limit, per-task timeout, child process cleanup, actor reaper. Daemon repo only.
5. **Unix socket & CLI** — socket server, status/health/sessions endpoints, CLI integration. Daemon repo only.

Subsystems 1 and 4 are the highest priority — they fix the reliability issues. Subsystem 2 is the highest-impact UX improvement. Subsystems 3 and 5 are important but can ship slightly later without blocking the core reliability fixes.
