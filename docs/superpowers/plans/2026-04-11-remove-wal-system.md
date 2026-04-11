# Remove WAL System Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the daemon's Write-Ahead Log (WAL) system and the relay's Ack/Replay machinery. The daemon keeps generating in-memory sequence numbers (the relay and browser UI depend on them for message ordering), but stops persisting them to disk. The relay stops sending Ack and ReplayRequest messages and stops querying `last_sequence` for the Welcome payload.

**Architecture:** The WAL was designed for crash-recovery replay but was never wired up (daemon discards the Welcome with a TODO). Meanwhile it caused a production outage: the Welcome payload grew past the 32KB WebSocket default as sessions accumulated. The relay already persists messages to Postgres — that's the durable log. The daemon becomes stateless: connect, stream, disconnect. Sequence numbers remain as in-memory monotonic counters for message ordering.

**Tech Stack:** Go 1.25 (daemon + relay + protocol-go), TypeScript (web app — read-only changes to understand what stays)

**Repos touched:**
- `gsd-build-daemon` — remove `internal/wal/` package, strip WAL from actor/manager/loop/client
- `gsd-build-cloud-app/apps/relay` — remove Ack sending, remove `SetLastSequence`/`GetLastSequencesByMachine` usage, simplify Hello/Welcome
- `gsd-build-protocol-go` — remove `Ack` and `ReplayRequest` types, remove `LastSequenceBySession` from Hello, remove `AckedSequencesBySession` from Welcome

**What stays:**
- `SequenceNumber` field on `protocol.Stream` — used by relay for DB insert ordering and by browser for chat rendering
- `sequence_number` column on `messages` table — used for cursor pagination and history reconstruction
- `last_sequence` column on `sessions` table — used by relay's `NextSequence()` for non-stream message sequencing
- In-memory `seq int64` counter in the daemon actor — still incremented per event, still set on Stream frames

**What goes:**
- `internal/wal/` package (4 files)
- WAL file I/O in actor (Open, Append, PruneUpTo, Close)
- `walDir`/`baseWALDir`/`WALPath` fields and initialization
- `wal.ScanDirectory()` call in connect flow
- `lastSequences` parameter on `client.Connect()`
- `handleAck()` and `handleReplay()` message handlers
- `PruneWAL()` method on Actor
- `LastSequenceBySession` field on Hello
- `AckedSequencesBySession` field on Welcome
- `Ack` and `ReplayRequest` protocol types + envelope cases
- Relay's Ack-sending code in `OnDaemonMessage`
- Relay's `SetLastSequence()` call (relay uses `NextSequence()` for non-stream; daemon-originated sequences come from the daemon's in-memory counter)
- Relay's `GetLastSequencesByMachine()` call

---

## Task 1: Remove WAL package from daemon

**Files:**
- Delete: `gsd-build-daemon/internal/wal/wal.go`
- Delete: `gsd-build-daemon/internal/wal/recover.go`
- Delete: `gsd-build-daemon/internal/wal/wal_test.go`
- Delete: `gsd-build-daemon/internal/wal/recover_test.go`

- [ ] **Step 1: Delete the WAL package**

```bash
rm -rf internal/wal/
```

- [ ] **Step 2: Verify no other package imports it yet (expect errors — we fix those in later tasks)**

```bash
go build ./... 2>&1 | head -20
```

Expected: compilation errors in `internal/loop` and `internal/session` referencing `wal` package. This confirms the deletion is correct and the remaining references are the ones we'll clean up next.

- [ ] **Step 3: Commit**

```bash
git add -A internal/wal/
git commit -m "remove: delete internal/wal package — WAL system no longer needed

The relay persists messages to Postgres. Local WAL files, pruning,
and sequence recovery are unnecessary complexity that caused a
production outage when the Welcome payload exceeded the WebSocket
read limit."
```

---

## Task 2: Strip WAL from session Actor

**Files:**
- Modify: `gsd-build-daemon/internal/session/actor.go`
- Modify: `gsd-build-daemon/internal/session/actor_test.go`

- [ ] **Step 1: Remove WAL imports and fields from actor.go**

Remove the `wal` import:
```go
// DELETE this import line:
"github.com/gsd-build/daemon/internal/wal"
```

Remove WAL-related fields from `Options` struct:
```go
// DELETE these two fields from Options:
WALPath  string
StartSeq int64
```

Remove WAL-related fields from `Actor` struct:
```go
// DELETE this field from Actor:
log *wal.Log
```

Keep `seq int64` in Actor — it's still used for in-memory sequence numbering.

- [ ] **Step 2: Simplify NewActor — remove WAL open**

Replace the NewActor function body. Remove the `wal.Open` call and the `log` field assignment. The `seq` field initializes to 0 (Go zero value).

Current code to replace:
```go
func NewActor(opts Options) (*Actor, error) {
	walLog, err := wal.Open(opts.WALPath)
	if err != nil {
		return nil, fmt.Errorf("open wal: %w", err)
	}

	return &Actor{
		opts:   opts,
		log:    walLog,
		seq:    opts.StartSeq,
		taskCh: make(chan protocol.Task, 1),
		stopCh: make(chan struct{}, 1),
		permCh: make(chan protocol.PermissionResponse, 1),
		qaCh:   make(chan protocol.QuestionResponse, 1),
	}, nil
}
```

New code:
```go
func NewActor(opts Options) (*Actor, error) {
	return &Actor{
		opts:   opts,
		taskCh: make(chan protocol.Task, 1),
		stopCh: make(chan struct{}, 1),
		permCh: make(chan protocol.PermissionResponse, 1),
		qaCh:   make(chan protocol.QuestionResponse, 1),
	}, nil
}
```

- [ ] **Step 3: Remove LastSequence method**

Delete the `LastSequence()` method entirely:
```go
// DELETE this method:
func (a *Actor) LastSequence() int64 {
	return a.seq
}
```

- [ ] **Step 4: Remove WAL append from event handling**

In the `exec.Run()` callback (around line 232), remove the WAL append. The sequence increment and Stream frame creation stay — only the file write goes.

Find and delete:
```go
		if err := a.log.Append(next, frameJSON); err != nil {
			return fmt.Errorf("wal append: %w", err)
		}
```

- [ ] **Step 5: Remove PruneWAL method**

Delete entirely:
```go
// DELETE this method:
func (a *Actor) PruneWAL(upTo int64) error {
	return a.log.PruneUpTo(upTo)
}
```

- [ ] **Step 6: Remove WAL close from Stop**

In the `Stop()` method, remove the `a.log.Close()` call. The method should just signal the stop channel and return nil (or whatever non-WAL cleanup remains).

Find and delete:
```go
	return a.log.Close()
```

Replace with:
```go
	return nil
```

- [ ] **Step 7: Update actor tests — remove WAL paths from Options**

In `actor_test.go`, remove `WALPath` and `StartSeq` from all `Options` struct literals. The tests create temp WAL directories that are no longer needed — remove those too.

For each test function that creates a `walDir`:
```go
// DELETE lines like:
walDir := t.TempDir()
// And remove from Options:
WALPath:  filepath.Join(walDir, "sess-1.jsonl"),
StartSeq: 0,
```

Also remove the `filepath` import if it's only used for WAL paths.

- [ ] **Step 8: Verify daemon builds and tests pass**

```bash
cd /path/to/gsd-build-daemon
go build ./...
go test ./internal/session/...
```

Expected: all pass, no WAL references remain in session package.

- [ ] **Step 9: Commit**

```bash
git add internal/session/
git commit -m "remove: strip WAL from session actor

Actor keeps in-memory sequence counter for Stream frame numbering.
WAL file open/append/prune/close removed. Options no longer need
WALPath or StartSeq."
```

---

## Task 3: Strip WAL from session Manager

**Files:**
- Modify: `gsd-build-daemon/internal/session/manager.go`

- [ ] **Step 1: Remove baseWALDir field and parameter**

Remove `baseWALDir string` from the `Manager` struct.

Update `NewManager` to remove the `baseWALDir` parameter:

Current signature:
```go
func NewManager(baseWALDir string, binaryPath string, client *relay.Client, verbosity display.VerbosityLevel) *Manager
```

New signature:
```go
func NewManager(binaryPath string, client *relay.Client, verbosity display.VerbosityLevel) *Manager
```

Remove the `baseWALDir` field assignment in the constructor body.

- [ ] **Step 2: Remove WAL path construction from Spawn**

In `Spawn()`, remove the code that constructs WALPath from baseWALDir:
```go
// DELETE these lines:
if opts.WALPath == "" {
	opts.WALPath = filepath.Join(m.baseWALDir, opts.SessionID+".jsonl")
}
```

Remove the `filepath` and `path/filepath` imports if no longer used.

- [ ] **Step 3: Remove LastSequences method**

Delete the `LastSequences()` method entirely — it was only used to populate the Hello message's `LastSequenceBySession` field:
```go
// DELETE this method:
func (m *Manager) LastSequences() map[string]int64 { ... }
```

- [ ] **Step 4: Verify build**

```bash
go build ./internal/session/...
```

Expected: fails because `internal/loop/daemon.go` still passes `walDir` to `NewManager`. That's expected — we fix it in Task 4.

- [ ] **Step 5: Commit**

```bash
git add internal/session/manager.go
git commit -m "remove: strip WAL from session manager

Manager no longer tracks baseWALDir or constructs per-session WAL
paths. LastSequences() removed."
```

---

## Task 4: Strip WAL from daemon loop and relay client

**Files:**
- Modify: `gsd-build-daemon/internal/loop/daemon.go`
- Modify: `gsd-build-daemon/internal/loop/daemon_test.go`
- Modify: `gsd-build-daemon/internal/relay/client.go`
- Modify: `gsd-build-daemon/internal/relay/client_test.go`

- [ ] **Step 1: Simplify client.Connect — remove lastSequences parameter**

In `client.go`, change `Connect` signature:

Current:
```go
func (c *Client) Connect(
	ctx context.Context,
	lastSequences map[string]int64,
) (*protocol.Welcome, error) {
```

New:
```go
func (c *Client) Connect(ctx context.Context) (*protocol.Welcome, error) {
```

In the Hello construction, remove the `LastSequenceBySession` field:

Current:
```go
hello := protocol.Hello{
	Type:                  protocol.MsgTypeHello,
	MachineID:             c.cfg.MachineID,
	DaemonVersion:         c.cfg.DaemonVersion,
	OS:                    c.cfg.OS,
	Arch:                  c.cfg.Arch,
	LastSequenceBySession: lastSequences,
}
```

New:
```go
hello := protocol.Hello{
	Type:          protocol.MsgTypeHello,
	MachineID:     c.cfg.MachineID,
	DaemonVersion: c.cfg.DaemonVersion,
	OS:            c.cfg.OS,
	Arch:          c.cfg.Arch,
}
```

Keep `conn.SetReadLimit(1 << 20)` (already added earlier).

- [ ] **Step 2: Simplify daemon.runOnce — remove ScanDirectory and lastSeqs**

In `daemon.go`, simplify `runOnce()`:

Current:
```go
func (d *Daemon) runOnce(ctx context.Context) error {
	lastSeqs, err := wal.ScanDirectory(d.walDir)
	if err != nil {
		return fmt.Errorf("scan wal directory: %w", err)
	}

	welcome, err := d.client.Connect(ctx, lastSeqs)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	_ = welcome // TODO: use acked sequences to drive WAL replay
```

New:
```go
func (d *Daemon) runOnce(ctx context.Context) error {
	if _, err := d.client.Connect(ctx); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
```

- [ ] **Step 3: Remove walDir from Daemon struct and initialization**

In `daemon.go`, remove `walDir string` from the `Daemon` struct.

In `NewWithBinaryPath()`:
- Remove `walDir := filepath.Join(home, "wal")`
- Update `session.NewManager(walDir, binaryPath, client, verbosity)` to `session.NewManager(binaryPath, client, verbosity)`
- Remove `walDir: walDir` from struct literal

Remove the `wal` import from daemon.go.

- [ ] **Step 4: Remove handleAck and handleReplay**

Delete both methods entirely from `daemon.go`:

```go
// DELETE handleAck (lines ~363-371):
func (d *Daemon) handleAck(msg *protocol.Ack) error { ... }

// DELETE handleReplay (lines ~373-396):
func (d *Daemon) handleReplay(msg *protocol.ReplayRequest) error { ... }
```

Remove their cases from `handleMessage()`:
```go
// DELETE these two cases:
case *protocol.Ack:
	return d.handleAck(msg)
case *protocol.ReplayRequest:
	return d.handleReplay(msg)
```

Remove unused imports: `"encoding/json"` (if only used by handleReplay), `"path/filepath"` (if only used for walDir).

- [ ] **Step 5: Update daemon_test.go**

Remove `TestHandleAckPlaceholder` and any WAL-related test helpers.

- [ ] **Step 6: Update client_test.go**

Update `TestClientConnectsAndSendsHello`:
- Change `Connect(ctx, map[string]int64{"sess-1": 5})` to `Connect(ctx)`
- Remove assertion on `hello.LastSequenceBySession`

- [ ] **Step 7: Update e2e tests**

In `tests/e2e/daemon_e2e_test.go`:
- Update Connect calls (remove lastSeqs parameter)
- Keep the Welcome send (tests still need it for the handshake)
- Remove any WAL directory assertions

- [ ] **Step 8: Verify full daemon builds and all tests pass**

```bash
cd /path/to/gsd-build-daemon
go build ./...
go test ./...
```

Expected: all pass.

- [ ] **Step 9: Commit**

```bash
git add internal/loop/ internal/relay/ tests/
git commit -m "remove: strip WAL from daemon loop and relay client

Connect no longer sends lastSequences in Hello or scans WAL directory.
Ack and ReplayRequest handlers removed. Daemon is stateless on reconnect."
```

---

## Task 5: Remove Ack sending and SetLastSequence from relay

**Files:**
- Modify: `gsd-build-cloud-app/apps/relay/internal/router/router.go`
- Modify: `gsd-build-cloud-app/apps/relay/internal/server/server.go`
- Modify: `gsd-build-cloud-app/apps/relay/internal/db/messages.go`
- Modify: `gsd-build-cloud-app/apps/relay/internal/db/sessions.go`
- Modify: `gsd-build-cloud-app/apps/relay/internal/db/messages_test.go`
- Modify: `gsd-build-cloud-app/apps/relay/internal/router/router_test.go`
- Modify: `gsd-build-cloud-app/apps/relay/internal/server/server_test.go`

- [ ] **Step 1: Remove Ack sending from router.go**

In `OnDaemonMessage`'s stream handler, delete the Ack block:

```go
// DELETE these lines (around line 126-133):
	// ACK back to daemon regardless — daemon needs to prune WAL even for non-persisted events
	if conn, ok := r.Daemons.GetByMachine(machineID); ok {
		_ = conn.Send(ctx, &protocol.Ack{
			Type:           protocol.MsgTypeAck,
			SessionID:      msg.SessionID,
			SequenceNumber: msg.SequenceNumber,
		})
	}
```

- [ ] **Step 2: Remove SetLastSequence call from router.go**

Delete the `SetLastSequence` call and its comment:

```go
// DELETE these lines (around line 114-118):
		// last_sequence advances ONLY on persisted events
		if err := r.Messages.SetLastSequence(ctx, msg.SessionID, msg.SequenceNumber); err != nil {
			r.Log.Error("set last_sequence failed", "err", err)
			return err
		}
```

Note: `last_sequence` column stays in the DB — it's still used by `NextSequence()` for non-stream message sequencing. We're only removing the daemon-driven updates. The relay's own `NextSequence()` increments `last_sequence` atomically when persisting non-stream messages, so the column remains self-consistent.

- [ ] **Step 3: Remove SetLastSequence and GetLastSequence from messages.go**

Delete `SetLastSequence()` function (~lines 58-71) and `GetLastSequence()` function (~lines 90-103). Keep `NextSequence()` — it's used by `persistNonStreamMessage`.

- [ ] **Step 4: Remove GetLastSequencesByMachine from sessions.go**

Delete the `GetLastSequencesByMachine()` function (~lines 67-94). It was only used by the Welcome handler, which is already sending an empty map.

- [ ] **Step 5: Simplify Welcome in server.go**

The Welcome is already simplified (from our earlier fix). Verify it looks like:

```go
	_ = sender.Send(ctx, &protocol.Welcome{
		Type:                    protocol.MsgTypeWelcome,
		AckedSequencesBySession: map[string]int64{},
	})
```

This stays as-is until protocol-go removes the field (Task 6), at which point this becomes just `Type`.

- [ ] **Step 6: Update tests**

In `messages_test.go`: delete `TestUpdateLastSequence` and `TestGetLastSequence` tests.

In `router_test.go` and `server_test.go`: remove any assertions about Ack messages being sent. Update tests that reference `SequenceNumber` on Stream messages — those stay, the field is still on the struct.

- [ ] **Step 7: Verify relay builds and all tests pass**

```bash
cd /path/to/gsd-build-cloud-app/apps/relay
go build ./...
go test ./...
```

Expected: all pass.

- [ ] **Step 8: Commit**

```bash
git add apps/relay/
git commit -m "remove: strip Ack sending and SetLastSequence from relay

Relay no longer sends Ack messages to daemon or updates last_sequence
from daemon-reported sequence numbers. NextSequence() retained for
non-stream message persistence. GetLastSequencesByMachine() removed."
```

---

## Task 6: Clean up protocol-go types

**Files:**
- Modify: `gsd-build-protocol-go/messages.go`
- Modify: `gsd-build-protocol-go/envelope.go`
- Modify: `gsd-build-protocol-go/messages_test.go`

**Important:** This is a breaking change to the wire protocol. Both the daemon and relay must be updated first (Tasks 1-5), then protocol-go is updated, then both consumers bump their `go.mod`.

- [ ] **Step 1: Remove Ack type and constant**

In `messages.go`, delete:
```go
MsgTypeAck           = "ack"
```

Delete the `Ack` struct:
```go
type Ack struct {
	Type           string `json:"type"`
	SessionID      string `json:"sessionId"`
	SequenceNumber int64  `json:"sequenceNumber"`
}
```

- [ ] **Step 2: Remove ReplayRequest type and constant**

In `messages.go`, delete:
```go
MsgTypeReplayRequest = "replayRequest"
```

Delete the `ReplayRequest` struct:
```go
type ReplayRequest struct {
	Type         string `json:"type"`
	SessionID    string `json:"sessionId"`
	FromSequence int64  `json:"fromSequence"`
}
```

- [ ] **Step 3: Remove LastSequenceBySession from Hello**

In `messages.go`, remove the field:
```go
// DELETE this field from Hello:
LastSequenceBySession map[string]int64 `json:"lastSequenceBySession"`
```

- [ ] **Step 4: Remove AckedSequencesBySession from Welcome**

In `messages.go`, remove the field:
```go
// DELETE this field from Welcome:
AckedSequencesBySession map[string]int64 `json:"ackedSequencesBySession"`
```

- [ ] **Step 5: Remove envelope parsing cases**

In `envelope.go`, delete:
```go
case MsgTypeAck:
	payload = &Ack{}
case MsgTypeReplayRequest:
	payload = &ReplayRequest{}
```

- [ ] **Step 6: Update test cases**

In `messages_test.go`:
- Delete the `"ack"` test case
- Remove `LastSequenceBySession` from the `"hello"` test case
- Update any Welcome test to remove `AckedSequencesBySession`

- [ ] **Step 7: Verify protocol-go builds and tests pass**

```bash
cd /path/to/gsd-build-protocol-go
go test ./...
```

- [ ] **Step 8: Tag and publish**

```bash
git add .
git commit -m "remove: Ack, ReplayRequest types and WAL fields from Hello/Welcome

Breaking change: daemon and relay must be updated before bumping to
this version. SequenceNumber on Stream is retained for message ordering."
git tag v0.X.0  # appropriate version bump
git push origin main --tags
```

---

## Task 7: Bump protocol-go in daemon and relay

**Files:**
- Modify: `gsd-build-daemon/go.mod`
- Modify: `gsd-build-cloud-app/apps/relay/go.mod`

- [ ] **Step 1: Bump daemon**

```bash
cd /path/to/gsd-build-daemon
go get github.com/gsd-build/protocol-go@v0.X.0
go mod tidy
```

- [ ] **Step 2: Fix compilation — remove references to deleted types**

The daemon should already be clean from Tasks 1-4, but verify:

```bash
go build ./...
go test ./...
```

Fix any remaining references to `protocol.Ack`, `protocol.ReplayRequest`, `LastSequenceBySession`, or `AckedSequencesBySession`.

- [ ] **Step 3: Bump relay**

```bash
cd /path/to/gsd-build-cloud-app/apps/relay
go get github.com/gsd-build/protocol-go@v0.X.0
go mod tidy
```

- [ ] **Step 4: Fix relay compilation**

Update `server.go` Welcome to remove `AckedSequencesBySession` (field no longer exists on the struct):

```go
// Old:
_ = sender.Send(ctx, &protocol.Welcome{
	Type:                    protocol.MsgTypeWelcome,
	AckedSequencesBySession: map[string]int64{},
})

// New:
_ = sender.Send(ctx, &protocol.Welcome{
	Type: protocol.MsgTypeWelcome,
})
```

Remove `protocol.MsgTypeAck` references if any remain.

```bash
go build ./...
go test ./...
```

- [ ] **Step 5: Commit both repos**

Daemon:
```bash
cd /path/to/gsd-build-daemon
git add go.mod go.sum
git commit -m "chore: bump protocol-go — Ack/ReplayRequest types removed"
```

Relay:
```bash
cd /path/to/gsd-build-cloud-app
git add apps/relay/go.mod apps/relay/go.sum
git commit -m "chore: bump protocol-go — Ack/ReplayRequest types removed"
```

---

## Task 8: Deploy and verify

- [ ] **Step 1: Deploy relay first**

The relay must go out first because it's backward-compatible: old daemons will send `LastSequenceBySession` in Hello (ignored as unknown JSON fields) and will receive an empty Welcome (which they already handle). The relay just stops sending Acks.

```bash
cd /path/to/gsd-build-cloud-app/apps/relay
fly deploy
```

- [ ] **Step 2: Verify relay health**

```bash
fly status -a gsd-relay
curl -s https://relay.gsd.build/health
```

Expected: `ok`

- [ ] **Step 3: Release daemon**

Tag and release the daemon binary. Existing daemons will get the update via the install script or manual upgrade.

- [ ] **Step 4: Test end-to-end**

```bash
gsd-cloud start
```

Expected: connects successfully, no "message too big" error, reconnects cleanly if interrupted.

- [ ] **Step 5: Clean up local WAL files (optional)**

Old WAL files in `~/.gsd-cloud/wal/` are now orphaned. They can be safely deleted:

```bash
rm -rf ~/.gsd-cloud/wal/
```

Consider adding a one-time cleanup to the daemon startup that removes the directory if it exists, with a log message.
