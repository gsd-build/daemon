# Warm Pi Provider Processes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build bounded warm Pi and provider process lifetimes in the daemon so recent active chats avoid cold starts without leaving unbounded child processes on user machines.

**Architecture:** A session actor owns one reusable `pi.Worker` for the active session/provider/config key. The worker starts one Pi RPC process, sends one prompt frame at a time, and remains idle until reused or evicted. The session manager enforces a 20-minute idle TTL, idle worker cap of 4, memory-pressure eviction, and deterministic process-group cleanup.

**Tech Stack:** Go 1.26 daemon internals, Pi RPC NDJSON, `github.com/gsd-build/protocol-go`, TypeScript Pi extension, Node-based extension tests.

---

## Scope

This plan implements daemon runtime process ownership and safety. Cloud-app provider selection UI and relay DB dispatch updates are separate product work. The daemon accepts `protocol.Task.Provider` and defaults empty provider to `claude-cli`.

## File Structure

- Create: `internal/pi/worker_key.go`
  - Defines `WorkerKey`, `WorkerSnapshot`, and `NewWorkerKey`.
- Create: `internal/pi/worker_key_test.go`
  - Verifies key equality, sorting, browser grant identity, and default provider behavior.
- Create: `internal/pi/worker.go`
  - Owns one warm Pi RPC process, prompt execution, process-group cleanup, and idle metadata.
- Create: `internal/pi/worker_test.go`
  - Tests process reuse, key mismatch behavior through keys, prompt cancellation cleanup, and no orphan child process behavior.
- Modify: `internal/pi/executor.go`
  - Shares process args, environment building, stderr draining, and process cleanup helpers with `Worker`.
- Modify: `internal/config/config.go`
  - Adds warm-worker TTL and idle-cap config fields plus effective/clamped accessors.
- Modify: `internal/config/config_test.go`
  - Covers defaults, clamp bounds, and explicit values.
- Modify: `internal/session/actor.go`
  - Owns one `pi.Worker`, computes worker keys, routes Pi tasks through warm worker when enabled, stops worker on actor stop and unsafe cancellation.
- Modify: `internal/session/actor_test.go`
  - Covers worker reuse, key mismatch restart, cancellation behavior, and actor stop cleanup.
- Modify: `internal/session/manager.go`
  - Adds worker-level reaping, idle cap LRU eviction, worker snapshots, and memory-pressure eviction.
- Modify: `internal/session/manager_test.go`
  - Covers TTL eviction, LRU cap eviction, memory-pressure eviction, and actor reaper worker cleanup.
- Modify: `internal/sockapi/provider.go`
  - Adds worker status fields to status and session snapshots.
- Modify: `internal/sockapi/handler_test.go`
  - Verifies worker status JSON shape.
- Modify: `internal/loop/daemon.go`
  - Starts worker reaper and includes worker status in daemon status.
- Modify: `internal/pi/extension/index.ts`
  - Wires warm Claude SDK worker behind `GSD_WARM_CLAUDE_SDK=1`.
- Create: `internal/pi/extension/claude-sdk-worker.ts`
  - Owns the long-lived Claude Agent SDK query pump.
- Create: `internal/pi/extension/claude-sdk-worker.test.mjs`
  - Verifies one fake Claude subprocess handles two provider turns.

## Task 1: Add Warm Worker Config

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write failing config tests**

Append these tests to `internal/config/config_test.go`:

```go
func TestEffectiveWarmWorkerIdle(t *testing.T) {
	cfg := &Config{}
	if got := cfg.EffectiveWarmWorkerIdle(); got != 20*time.Minute {
		t.Fatalf("default idle = %s, want 20m", got)
	}

	cfg.WarmWorkerIdleMinutes = 1
	if got := cfg.EffectiveWarmWorkerIdle(); got != 2*time.Minute {
		t.Fatalf("low clamp idle = %s, want 2m", got)
	}

	cfg.WarmWorkerIdleMinutes = 90
	if got := cfg.EffectiveWarmWorkerIdle(); got != 60*time.Minute {
		t.Fatalf("high clamp idle = %s, want 60m", got)
	}

	cfg.WarmWorkerIdleMinutes = 15
	if got := cfg.EffectiveWarmWorkerIdle(); got != 15*time.Minute {
		t.Fatalf("explicit idle = %s, want 15m", got)
	}
}

func TestEffectiveWarmWorkerIdleCap(t *testing.T) {
	cfg := &Config{}
	if got := cfg.EffectiveWarmWorkerIdleCap(); got != 4 {
		t.Fatalf("default cap = %d, want 4", got)
	}

	cfg.WarmWorkerIdleCap = ptr(-1)
	if got := cfg.EffectiveWarmWorkerIdleCap(); got != 0 {
		t.Fatalf("low clamp cap = %d, want 0", got)
	}

	cfg.WarmWorkerIdleCap = ptr(40)
	if got := cfg.EffectiveWarmWorkerIdleCap(); got != 16 {
		t.Fatalf("high clamp cap = %d, want 16", got)
	}

	cfg.WarmWorkerIdleCap = ptr(7)
	if got := cfg.EffectiveWarmWorkerIdleCap(); got != 7 {
		t.Fatalf("explicit cap = %d, want 7", got)
	}

	cfg.WarmWorkerIdleCap = ptr(0)
	if got := cfg.EffectiveWarmWorkerIdleCap(); got != 0 {
		t.Fatalf("explicit zero cap = %d, want 0", got)
	}
}

func ptr[T any](v T) *T { return &v }
```

- [ ] **Step 2: Run config tests to verify failure**

Run:

```bash
go test ./internal/config -run 'TestEffectiveWarmWorker' -count=1
```

Expected: FAIL with `cfg.EffectiveWarmWorkerIdle undefined` and `cfg.EffectiveWarmWorkerIdleCap undefined`.

- [ ] **Step 3: Add config fields and accessors**

Modify `internal/config/config.go`:

```go
type Config struct {
	MachineID                string `json:"machineId"`
	AuthToken                string `json:"authToken"`
	TokenExpiresAt           string `json:"tokenExpiresAt,omitempty"`
	ServerURL                string `json:"serverUrl"`
	RelayURL                 string `json:"relayUrl"`
	MaxConcurrentTasks       int    `json:"maxConcurrentTasks,omitempty"` // 0 means runtime.NumCPU
	TaskTimeoutMinutes       int    `json:"taskTimeoutMinutes,omitempty"`
	WarmWorkerIdleMinutes    int    `json:"warmWorkerIdleMinutes,omitempty"`
	WarmWorkerIdleCap        *int   `json:"warmWorkerIdleCap,omitempty"`
	LogLevel                 string `json:"logLevel,omitempty"`
}
```

Add constants near `DefaultTaskTimeoutMinutes`:

```go
const DefaultWarmWorkerIdleMinutes = 20
const MinWarmWorkerIdleMinutes = 2
const MaxWarmWorkerIdleMinutes = 60
const DefaultWarmWorkerIdleCap = 4
const MinWarmWorkerIdleCap = 0
const MaxWarmWorkerIdleCap = 16
```

Add helpers after `EffectiveTaskTimeout`:

```go
func (c *Config) EffectiveWarmWorkerIdle() time.Duration {
	minutes := c.WarmWorkerIdleMinutes
	if minutes == 0 {
		minutes = DefaultWarmWorkerIdleMinutes
	}
	if minutes < MinWarmWorkerIdleMinutes {
		minutes = MinWarmWorkerIdleMinutes
	}
	if minutes > MaxWarmWorkerIdleMinutes {
		minutes = MaxWarmWorkerIdleMinutes
	}
	return time.Duration(minutes) * time.Minute
}

func (c *Config) EffectiveWarmWorkerIdleCap() int {
	if c.WarmWorkerIdleCap == nil {
		return DefaultWarmWorkerIdleCap
	}
	cap := *c.WarmWorkerIdleCap
	if cap < MinWarmWorkerIdleCap {
		return MinWarmWorkerIdleCap
	}
	if cap > MaxWarmWorkerIdleCap {
		return MaxWarmWorkerIdleCap
	}
	return cap
}
```

- [ ] **Step 4: Run config tests**

Run:

```bash
go test ./internal/config -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit config**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: add warm worker config"
```

## Task 2: Define Worker Identity And Snapshots

**Files:**
- Create: `internal/pi/worker_key.go`
- Create: `internal/pi/worker_key_test.go`

- [ ] **Step 1: Write worker key tests**

Create `internal/pi/worker_key_test.go`:

```go
package pi

import (
	"testing"

	protocol "github.com/gsd-build/protocol-go"
)

func TestNewWorkerKeyDefaultsProviderAndSortsSkills(t *testing.T) {
	a := NewWorkerKey(Options{
		BinaryPath:    "/bin/pi",
		CWD:           "/repo",
		Model:         "claude-sonnet-4-6",
		ResumeSession: "/tmp/session.jsonl",
		ExtensionPath: "/ext/index.ts",
		SkillPaths:    []string{"/skills/b", "/skills/a"},
	})
	b := NewWorkerKey(Options{
		BinaryPath:    "/bin/pi",
		CWD:           "/repo",
		Model:         "claude-sonnet-4-6",
		ResumeSession: "/tmp/session.jsonl",
		ExtensionPath: "/ext/index.ts",
		Provider:      "claude-cli",
		SkillPaths:    []string{"/skills/a", "/skills/b"},
	})

	if a != b {
		t.Fatalf("keys differ after default provider and skill sorting:\na=%+v\nb=%+v", a, b)
	}
}

func TestWorkerKeyIncludesBrowserGrant(t *testing.T) {
	base := Options{
		BinaryPath:       "/bin/pi",
		CWD:              "/repo",
		Model:            "claude-sonnet-4-6",
		ResumeSession:    "/tmp/session.jsonl",
		ExtensionPath:    "/ext/index.ts",
		Provider:         "claude-cli",
		BrowserGrantID:   "grant-1",
		BrowserID:        "browser-1",
		BrowserSessionID: "session-1",
	}
	a := NewWorkerKey(base)

	cases := []struct {
		name string
		edit func(*Options)
	}{
		{name: "grant", edit: func(opts *Options) { opts.BrowserGrantID = "grant-2" }},
		{name: "browser", edit: func(opts *Options) { opts.BrowserID = "browser-2" }},
		{name: "session", edit: func(opts *Options) { opts.BrowserSessionID = "session-2" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			other := base
			tc.edit(&other)
			if a == NewWorkerKey(other) {
				t.Fatalf("worker key ignored browser %s", tc.name)
			}
		})
	}
}
```

- [ ] **Step 2: Run worker key tests to verify failure**

Run:

```bash
go test ./internal/pi -run TestWorkerKey -count=1
```

Expected: FAIL because `NewWorkerKey` is undefined.

- [ ] **Step 3: Implement worker identity**

Create `internal/pi/worker_key.go`:

```go
package pi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

type WorkerKey struct {
	BinaryPath         string
	CWD                string
	Model              string
	ResumeSession      string
	CustomInstructions string
	ExtensionPath      string
	Provider           string
	SkillPaths         string
	BrowserGrantID     string
	BrowserID          string
	BrowserSessionID   string
}

type WorkerSnapshot struct {
	SessionID string
	Provider  string
	Model     string
	PID       int
	KeyHash   string
	State     string
	StartedAt time.Time
	LastUsedAt time.Time
	IdleSince *time.Time
}

func NewWorkerKey(opts Options) WorkerKey {
	skills := append([]string(nil), opts.SkillPaths...)
	sort.Strings(skills)
	key := WorkerKey{
		BinaryPath:         opts.BinaryPath,
		CWD:                opts.CWD,
		Model:              opts.Model,
		ResumeSession:      opts.ResumeSession,
		CustomInstructions: strings.TrimSpace(opts.CustomInstructions),
		ExtensionPath:      opts.ExtensionPath,
		Provider:           ProviderOrDefault(opts.Provider),
		SkillPaths:         strings.Join(skills, "\x00"),
		BrowserGrantID:     opts.BrowserGrantID,
		BrowserID:          opts.BrowserID,
		BrowserSessionID:   opts.BrowserSessionID,
	}
	return key
}

func (k WorkerKey) Hash() string {
	data, _ := json.Marshal(k)
	return hashString(string(data))
}

func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 4: Run worker key tests**

Run:

```bash
go test ./internal/pi -run TestWorkerKey -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit worker identity**

```bash
git add internal/pi/worker_key.go internal/pi/worker_key_test.go
git commit -m "feat: define pi worker identity"
```

## Task 3: Build The Warm Pi Worker

**Files:**
- Create: `internal/pi/worker.go`
- Create: `internal/pi/worker_test.go`
- Modify: `internal/pi/executor.go`

- [ ] **Step 1: Write a two-prompt worker test**

Create `internal/pi/worker_test.go`:

```go
package pi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gsd-build/daemon/internal/claude"
)

func writeWarmFakePi(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "pi")
	script := `#!/bin/sh
count=0
while IFS= read -r line; do
  case "$line" in
    *'"type":"prompt"'*)
      count=$((count + 1))
      printf '%s\n' '{"type":"response","command":"prompt","success":true}'
      printf '%s\n' "{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_delta\",\"contentIndex\":0,\"delta\":\"turn-$count\"},\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"turn-$count\"}],\"api\":\"anthropic-messages\",\"provider\":\"claude-cli\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input\":0,\"output\":0,\"cacheRead\":0,\"cacheWrite\":0,\"totalTokens\":0,\"cost\":{\"total\":0}}}}"
      printf '%s\n' "{\"type\":\"agent_end\",\"messages\":[{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"turn-$count\"}],\"usage\":{\"input\":1,\"output\":1,\"cacheRead\":0,\"cacheWrite\":0,\"cost\":{\"total\":0.001}}}]}"
      ;;
  esac
done
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}
	return path
}

func TestWorkerReusesOnePiProcessForTwoPrompts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	worker := NewWorker(Options{
		BinaryPath:    writeWarmFakePi(t),
		CWD:           t.TempDir(),
		ExtensionPath: filepath.Join(t.TempDir(), "index.ts"),
		Provider:      "claude-cli",
		Model:         "claude-sonnet-4-6",
	})
	if err := os.WriteFile(worker.opts.ExtensionPath, []byte("// fake"), 0600); err != nil {
		t.Fatalf("write extension: %v", err)
	}
	defer worker.Stop(context.Background())

	var first []string
	if err := worker.Prompt(ctx, PromptRequest{
		TaskID:  "task-1",
		Message: "first",
		OnEvent: func(e claude.Event) error {
			if e.Type == "stream_event" {
				first = append(first, string(e.Raw))
			}
			return nil
		},
	}); err != nil {
		t.Fatalf("first prompt: %v", err)
	}
	firstPID := worker.PID()

	var second []string
	if err := worker.Prompt(ctx, PromptRequest{
		TaskID:  "task-2",
		Message: "second",
		OnEvent: func(e claude.Event) error {
			if e.Type == "stream_event" {
				second = append(second, string(e.Raw))
			}
			return nil
		},
	}); err != nil {
		t.Fatalf("second prompt: %v", err)
	}

	if worker.PID() != firstPID {
		t.Fatalf("worker PID changed: first=%d second=%d", firstPID, worker.PID())
	}
	if !strings.Contains(strings.Join(first, "\n"), "turn-1") {
		t.Fatalf("first prompt did not stream turn-1: %#v", first)
	}
	if !strings.Contains(strings.Join(second, "\n"), "turn-2") {
		t.Fatalf("second prompt did not stream turn-2: %#v", second)
	}
}
```

- [ ] **Step 2: Run worker test to verify failure**

Run:

```bash
go test ./internal/pi -run TestWorkerReusesOnePiProcessForTwoPrompts -count=1
```

Expected: FAIL because `NewWorker` and `PromptRequest` are undefined.

- [ ] **Step 3: Extract shared process args helper**

In `internal/pi/executor.go`, add:

```go
func processArgs(opts Options) []string {
	args := []string{
		"-e", opts.ExtensionPath,
		"--provider", ProviderOrDefault(opts.Provider),
		"--no-extensions", "--no-prompt-templates",
		"--offline",
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if customInstructions := strings.TrimSpace(opts.CustomInstructions); customInstructions != "" {
		args = append(args, "--append-system-prompt", customInstructions)
	}
	for _, path := range opts.SkillPaths {
		if path != "" {
			args = append(args, "--skill", path)
		}
	}
	return args
}

func processEnv(base []string, opts Options) []string {
	return browserEnv(base, opts)
}
```

Update `Executor.Run` to use `processArgs(e.opts)` and `processEnv(os.Environ(), e.opts)`.

- [ ] **Step 4: Implement worker**

Create `internal/pi/worker.go`:

```go
package pi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/gsd-build/daemon/internal/claude"
)

type PromptRequest struct {
	TaskID               string
	Message              string
	OnEvent              func(claude.Event) error
	OnUIRequest          UIRequestHandler
	OnToolExecutionStart func(ToolExecutionStart)
	OnToolExecutionEnd   func(ToolExecutionEnd)
}

type Worker struct {
	opts Options
	key  WorkerKey

	mu        sync.Mutex
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.Reader
	stderrBuf []byte
	stderrDone chan struct{}
	pid       int
	startedAt time.Time
	lastUsedAt time.Time
	idleSince *time.Time
	broken    bool
	running   bool

	OnPIDStart func(pid int)
	OnPIDExit  func(pid int)
}

func NewWorker(opts Options) *Worker {
	if opts.BinaryPath == "" {
		opts.BinaryPath = "pi"
	}
	opts.Provider = ProviderOrDefault(opts.Provider)
	return &Worker{opts: opts, key: NewWorkerKey(opts)}
}

func (w *Worker) Key() WorkerKey {
	return w.key
}

func (w *Worker) PID() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.pid
}

func (w *Worker) IsIdle() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cmd != nil && !w.running && !w.broken
}

func (w *Worker) Snapshot(sessionID string) WorkerSnapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	state := "stopped"
	if w.cmd != nil && w.running {
		state = "executing"
	} else if w.cmd != nil && !w.broken {
		state = "idle"
	} else if w.broken {
		state = "broken"
	}
	return WorkerSnapshot{
		SessionID: sessionID,
		Provider:  w.opts.Provider,
		Model:     w.opts.Model,
		PID:       w.pid,
		KeyHash:   w.key.Hash(),
		State:     state,
		StartedAt: w.startedAt,
		LastUsedAt: w.lastUsedAt,
		IdleSince: w.idleSince,
	}
}

func (w *Worker) startLocked(ctx context.Context) error {
	if w.cmd != nil && !w.broken {
		return nil
	}
	if w.opts.ExtensionPath == "" {
		return fmt.Errorf("pi extension path is required")
	}
	if _, err := os.Stat(w.opts.ExtensionPath); err != nil {
		return fmt.Errorf("pi extension not found at %s: %w", w.opts.ExtensionPath, err)
	}

	cmd := piRPCCommand(ctx, w.opts.BinaryPath, w.opts.CWD, w.opts.ResumeSession, processArgs(w.opts)...)
	cmd.Env = processEnv(os.Environ(), w.opts)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start pi: %w", err)
	}

	w.cmd = cmd
	w.stdin = stdin
	w.stdout = stdout
	w.pid = cmd.Process.Pid
	now := time.Now()
	w.startedAt = now
	w.lastUsedAt = now
	w.idleSince = &now
	w.broken = false
	w.stderrBuf = nil
	w.stderrDone = make(chan struct{})
	go w.drainStderr(stderr, w.stderrDone)

	if w.OnPIDStart != nil {
		w.OnPIDStart(w.pid)
	}
	slog.Info("pi_worker_start", "pid", w.pid, "provider", w.opts.Provider, "model", w.opts.Model, "key", w.key.Hash())
	return nil
}

func (w *Worker) drainStderr(stderr io.Reader, done chan<- struct{}) {
	defer close(done)
	buf := make([]byte, 4096)
	for {
		n, err := stderr.Read(buf)
		w.mu.Lock()
		if n > 0 {
			w.stderrBuf = append(w.stderrBuf, buf[:n]...)
			if len(w.stderrBuf) > 16*1024 {
				w.stderrBuf = w.stderrBuf[len(w.stderrBuf)-16*1024:]
			}
		}
		w.mu.Unlock()
		if err != nil {
			return
		}
	}
}

func (w *Worker) Prompt(ctx context.Context, req PromptRequest) error {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return fmt.Errorf("pi worker already has an active prompt")
	}
	if err := w.startLocked(ctx); err != nil {
		w.mu.Unlock()
		return err
	}
	w.running = true
	w.idleSince = nil
	w.lastUsedAt = time.Now()
	stdin := w.stdin
	stdout := w.stdout
	startedAt := time.Now()
	pid := w.pid
	w.mu.Unlock()

	defer func() {
		w.mu.Lock()
		w.running = false
		now := time.Now()
		w.lastUsedAt = now
		w.idleSince = &now
		w.mu.Unlock()
	}()

	doneWatching := make(chan struct{})
	defer close(doneWatching)
	go func() {
		select {
		case <-ctx.Done():
			w.markBroken()
			_ = syscall.Kill(-pid, syscall.SIGTERM)
		case <-doneWatching:
		}
	}()

	frame, _ := json.Marshal(map[string]any{
		"id":      "task-" + req.TaskID,
		"type":    "prompt",
		"message": req.Message,
	})
	if _, err := stdin.Write(append(frame, '\n')); err != nil {
		w.markBroken()
		return fmt.Errorf("write prompt frame: %w", err)
	}

	agentEndCh := make(chan struct{}, 1)
	state := &translatorState{
		cwd:    w.opts.CWD,
		model:  w.opts.Model,
		taskID: req.TaskID,
	}
	err := streamPiEvents(
		ctx,
		stdout,
		stdin,
		req.OnEvent,
		req.OnUIRequest,
		req.OnToolExecutionStart,
		req.OnToolExecutionEnd,
		agentEndCh,
		true,
		state,
		startedAt,
	)
	if err != nil {
		w.markBroken()
		return err
	}
	return nil
}

func (w *Worker) markBroken() {
	w.mu.Lock()
	w.broken = true
	w.mu.Unlock()
}

func (w *Worker) Stop(ctx context.Context) error {
	w.mu.Lock()
	cmd := w.cmd
	pid := w.pid
	stdin := w.stdin
	done := w.stderrDone
	w.cmd = nil
	w.stdin = nil
	w.stdout = nil
	w.pid = 0
	w.running = false
	w.broken = true
	w.idleSince = nil
	w.mu.Unlock()

	if cmd == nil {
		return nil
	}
	err := terminateProcessGroupAndWait(cmd, pid, stdin, 2*time.Second)
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if w.OnPIDExit != nil {
		w.OnPIDExit(pid)
	}
	slog.Info("pi_worker_stop", "pid", pid, "provider", w.opts.Provider, "model", w.opts.Model)
	return err
}
```

- [ ] **Step 5: Run worker tests**

Run:

```bash
go test ./internal/pi -run TestWorkerReusesOnePiProcessForTwoPrompts -count=1
```

Expected: PASS.

- [ ] **Step 6: Run all Pi package tests**

Run:

```bash
go test ./internal/pi -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit worker implementation**

```bash
git add internal/pi/executor.go internal/pi/worker.go internal/pi/worker_test.go
git commit -m "feat: add warm pi worker"
```

## Task 4: Route Actor Pi Tasks Through The Worker

**Files:**
- Modify: `internal/session/actor.go`
- Modify: `internal/session/actor_test.go`

- [ ] **Step 1: Write actor reuse test**

Add this test to `internal/session/actor_test.go`:

```go
func TestActorReusesWarmPiWorkerForMatchingTasks(t *testing.T) {
	relay := newFakeRelay()
	actor, err := NewActor(testPiOptions(t, Options{
		SessionID: "sess-warm",
		CWD:       t.TempDir(),
		Relay:     relay,
	}))
	if err != nil {
		t.Fatalf("NewActor: %v", err)
	}
	actor.useWarmPiWorker = true

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		_ = actor.Run(ctx)
	}()

	if err := actor.SendTask(protocol.Task{TaskID: "t1", SessionID: "sess-warm", ChannelID: "ch", Prompt: "first", Engine: "pi"}); err != nil {
		t.Fatalf("send t1: %v", err)
	}
	if !relay.waitForTaskComplete(t, 5*time.Second) {
		t.Fatal("t1 did not complete")
	}
	firstPID := actor.workerPIDForTest()
	if firstPID == 0 {
		t.Fatal("expected warm worker PID after first task")
	}

	if err := actor.SendTask(protocol.Task{TaskID: "t2", SessionID: "sess-warm", ChannelID: "ch", Prompt: "second", Engine: "pi"}); err != nil {
		t.Fatalf("send t2: %v", err)
	}
	if !relay.waitForTaskComplete(t, 5*time.Second) {
		t.Fatal("t2 did not complete")
	}
	if got := actor.workerPIDForTest(); got != firstPID {
		t.Fatalf("worker PID = %d, want reused PID %d", got, firstPID)
	}
}
```

- [ ] **Step 2: Run actor test to verify failure**

Run:

```bash
go test ./internal/session -run TestActorReusesWarmPiWorkerForMatchingTasks -count=1
```

Expected: FAIL because `useWarmPiWorker` and `workerPIDForTest` are undefined.

- [ ] **Step 3: Add actor worker fields**

Modify `Actor` in `internal/session/actor.go`:

```go
	piWorkerMu     sync.Mutex
	piWorker       *pi.Worker
	piWorkerKey    pi.WorkerKey
	useWarmPiWorker bool
```

Set `useWarmPiWorker` in `NewActor` from the environment:

```go
actor.useWarmPiWorker = os.Getenv("GSD_WARM_PI_WORKERS") == "1"
```

Add helper methods:

```go
func (a *Actor) workerPIDForTest() int {
	a.piWorkerMu.Lock()
	defer a.piWorkerMu.Unlock()
	if a.piWorker == nil {
		return 0
	}
	return a.piWorker.PID()
}

func (a *Actor) stopPiWorker(ctx context.Context) {
	a.piWorkerMu.Lock()
	worker := a.piWorker
	a.piWorker = nil
	a.piWorkerMu.Unlock()
	if worker != nil {
		_ = worker.Stop(ctx)
	}
}
```

- [ ] **Step 4: Route `runPiExecutor` through warm worker**

In `runPiExecutor`, construct `opts := pi.Options{...}` once. Then use this branch before constructing `pi.NewExecutor`:

```go
	if a.useWarmPiWorker {
		return a.runPiWorker(actorCtx, taskCtx, tc, prompt, opts, coordinator)
	}
```

Add `runPiWorker`:

```go
func (a *Actor) runPiWorker(actorCtx context.Context, taskCtx context.Context, tc *taskContext, prompt string, opts pi.Options, coordinator *structuredQuestionCoordinator) error {
	key := pi.NewWorkerKey(opts)
	a.piWorkerMu.Lock()
	worker := a.piWorker
	if worker != nil && worker.Key() != key {
		a.piWorker = nil
		a.piWorkerMu.Unlock()
		_ = worker.Stop(actorCtx)
		a.piWorkerMu.Lock()
		worker = nil
	}
	if worker == nil {
		worker = pi.NewWorker(opts)
		a.attachPiWorkerPIDCallbacks(worker, tc.TaskID)
		a.piWorker = worker
		a.piWorkerKey = key
	}
	a.piWorkerMu.Unlock()

	resultRaw, err := a.forwardExecutorEvents(actorCtx, taskCtx, tc, func(ctx context.Context, onEvent func(claude.Event) error) error {
		return worker.Prompt(ctx, pi.PromptRequest{
			TaskID:               tc.TaskID,
			Message:              prompt,
			OnEvent:              onEvent,
			OnUIRequest:          a.makePiUIHandler(ctx, tc, coordinator),
			OnToolExecutionStart: a.capturePiToolStart(coordinator),
			OnToolExecutionEnd:   a.capturePiToolEnd(tc.ChannelID),
		})
	})
	if err != nil {
		a.stopPiWorker(actorCtx)
		return err
	}
	return a.handleResult(taskCtx, tc, resultRaw)
}
```

Add PID callback helper:

```go
func (a *Actor) attachPiWorkerPIDCallbacks(worker *pi.Worker, taskID string) {
	worker.OnPIDStart = func(pid int) {
		if a.pidDir != "" {
			_ = pidfile.Write(a.pidDir, taskID, pid)
		}
	}
	worker.OnPIDExit = func(pid int) {
		if a.pidDir != "" {
			_ = pidfile.Remove(a.pidDir, taskID)
		}
	}
}
```

- [ ] **Step 5: Stop worker on actor stop**

Modify `Actor.Stop`:

```go
func (a *Actor) Stop() error {
	a.stopPiWorker(context.Background())
	select {
	case <-a.stopCh:
	default:
		close(a.stopCh)
	}
	return nil
}
```

- [ ] **Step 6: Run actor tests**

Run:

```bash
go test ./internal/session -run 'TestActorReusesWarmPiWorkerForMatchingTasks|TestCancelTask_ActorStaysAlive' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit actor integration**

```bash
git add internal/session/actor.go internal/session/actor_test.go
git commit -m "feat: route pi tasks through warm worker"
```

## Task 5: Add Worker Eviction And Status

**Files:**
- Modify: `internal/session/actor.go`
- Modify: `internal/session/manager.go`
- Modify: `internal/session/manager_test.go`
- Modify: `internal/sockapi/provider.go`
- Modify: `internal/sockapi/handler_test.go`
- Modify: `internal/loop/daemon.go`

- [ ] **Step 1: Write manager eviction selection tests**

Add tests to `internal/session/manager_test.go`:

```go
func TestSelectIdleWorkerVictimsStopsExpiredWorkers(t *testing.T) {
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	victims := selectIdleWorkerVictims([]pi.WorkerSnapshot{
		{SessionID: "s1", State: "idle", LastUsedAt: now.Add(-25 * time.Minute)},
		{SessionID: "s2", State: "idle", LastUsedAt: now.Add(-5 * time.Minute)},
		{SessionID: "s3", State: "executing", LastUsedAt: now.Add(-30 * time.Minute)},
	}, now, 20*time.Minute, 4, false)

	if got := victims["s1"]; got != "ttl" {
		t.Fatalf("victims[s1] = %q, want ttl", got)
	}
	if _, ok := victims["s2"]; ok {
		t.Fatal("fresh idle worker selected as victim")
	}
	if _, ok := victims["s3"]; ok {
		t.Fatal("executing worker selected as victim")
	}
}

func TestSelectIdleWorkerVictimsUsesLRUCap(t *testing.T) {
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	victims := selectIdleWorkerVictims([]pi.WorkerSnapshot{
		{SessionID: "oldest", State: "idle", LastUsedAt: now.Add(-3 * time.Minute)},
		{SessionID: "middle", State: "idle", LastUsedAt: now.Add(-2 * time.Minute)},
		{SessionID: "newest", State: "idle", LastUsedAt: now.Add(-1 * time.Minute)},
	}, now, 20*time.Minute, 1, false)

	if got := victims["oldest"]; got != "lru" {
		t.Fatalf("victims[oldest] = %q, want lru", got)
	}
	if got := victims["middle"]; got != "lru" {
		t.Fatalf("victims[middle] = %q, want lru", got)
	}
	if _, ok := victims["newest"]; ok {
		t.Fatal("newest idle worker selected as victim")
	}
}

func TestSelectIdleWorkerVictimsEvictsAllIdleUnderMemoryPressure(t *testing.T) {
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	victims := selectIdleWorkerVictims([]pi.WorkerSnapshot{
		{SessionID: "idle-1", State: "idle", LastUsedAt: now.Add(-1 * time.Minute)},
		{SessionID: "idle-2", State: "idle", LastUsedAt: now.Add(-2 * time.Minute)},
		{SessionID: "busy", State: "executing", LastUsedAt: now.Add(-30 * time.Minute)},
	}, now, 20*time.Minute, 4, true)

	if got := victims["idle-1"]; got != "memory" {
		t.Fatalf("victims[idle-1] = %q, want memory", got)
	}
	if got := victims["idle-2"]; got != "memory" {
		t.Fatalf("victims[idle-2] = %q, want memory", got)
	}
	if _, ok := victims["busy"]; ok {
		t.Fatal("executing worker selected under memory pressure")
	}
}
```

- [ ] **Step 2: Run manager tests to verify failure**

Run:

```bash
go test ./internal/session -run 'TestSelectIdleWorkerVictims' -count=1
```

Expected: FAIL because `selectIdleWorkerVictims` is undefined.

- [ ] **Step 3: Add actor worker snapshot helpers**

In `internal/session/actor.go`, add production helpers plus test helpers:

```go
func (a *Actor) WorkerSnapshot() (pi.WorkerSnapshot, bool) {
	a.piWorkerMu.Lock()
	defer a.piWorkerMu.Unlock()
	if a.piWorker == nil {
		return pi.WorkerSnapshot{}, false
	}
	return a.piWorker.Snapshot(a.opts.SessionID), true
}

func (a *Actor) StopIdleWorker(ctx context.Context, reason string) bool {
	a.piWorkerMu.Lock()
	worker := a.piWorker
	if worker == nil || !worker.IsIdle() {
		a.piWorkerMu.Unlock()
		return false
	}
	a.piWorker = nil
	a.piWorkerMu.Unlock()
	_ = worker.Stop(ctx)
	slog.Info("pi_worker_evict", "sessionId", a.opts.SessionID, "reason", reason)
	return true
}
```

- [ ] **Step 4: Add post-task worker reaping hook**

Add a completion hook to `Options` in `internal/session/actor.go`:

```go
type Options struct {
	SessionID         string
	CWD               string
	Relay             RelaySender
	Model             string
	Effort            string
	PermissionMode    string
	ResumeSession     string
	PiBinaryPath      string
	PiExtensionPath   string
	Uploader          ImageUploader
	BrowserGrantID    string
	BrowserID         string
	RecordTouchedFile func(channelID string, cwd string, path string)
	OnTaskIdle        func()
}
```

Call the hook from `executeTask` after task state is marked idle:

```go
defer func() {
	cancel()
	a.taskMu.Lock()
	a.taskCancel = nil
	a.taskID = ""
	a.taskStartedAt = nil
	idleNow := time.Now()
	a.idleSince = &idleNow
	a.lastActiveAt = idleNow
	a.taskMu.Unlock()
	if a.opts.OnTaskIdle != nil {
		a.opts.OnTaskIdle()
	}
}()
```

- [ ] **Step 5: Add manager worker reaper**

In `internal/session/manager.go`, add:

```go
func selectIdleWorkerVictims(snapshots []pi.WorkerSnapshot, now time.Time, maxIdle time.Duration, idleCap int, memoryPressure bool) map[string]string {
	victims := make(map[string]string)
	idle := make([]pi.WorkerSnapshot, 0, len(snapshots))
	for _, snap := range snapshots {
		if snap.State != "idle" {
			continue
		}
		if memoryPressure {
			victims[snap.SessionID] = "memory"
			continue
		}
		if now.Sub(snap.LastUsedAt) >= maxIdle {
			victims[snap.SessionID] = "ttl"
			continue
		}
		idle = append(idle, snap)
	}
	sort.Slice(idle, func(i, j int) bool {
		return idle[i].LastUsedAt.Before(idle[j].LastUsedAt)
	})
	for len(idle) > idleCap {
		victim := idle[0]
		idle = idle[1:]
		victims[victim.SessionID] = "lru"
	}
	return victims
}

func (m *Manager) WorkerSnapshots() []pi.WorkerSnapshot {
	m.mu.Lock()
	actors := make([]*Actor, 0, len(m.actors))
	for _, a := range m.actors {
		actors = append(actors, a)
	}
	m.mu.Unlock()

	var out []pi.WorkerSnapshot
	for _, a := range actors {
		if snap, ok := a.WorkerSnapshot(); ok {
			out = append(out, snap)
		}
	}
	return out
}

func (m *Manager) ReapIdleWorkers(maxIdle time.Duration, idleCap int) int {
	m.mu.Lock()
	actors := make(map[string]*Actor, len(m.actors))
	for id, a := range m.actors {
		actors[id] = a
	}
	m.mu.Unlock()

	snaps := make([]pi.WorkerSnapshot, 0, len(actors))
	for _, a := range actors {
		if snap, ok := a.WorkerSnapshot(); ok {
			snaps = append(snaps, snap)
		}
	}
	victims := selectIdleWorkerVictims(snaps, time.Now(), maxIdle, idleCap, memoryTooLow())
	reaped := 0
	for sessionID, reason := range victims {
		actor := actors[sessionID]
		if actor != nil && actor.StopIdleWorker(context.Background(), reason) {
			reaped++
		}
	}
	return reaped
}

func (m *Manager) StartWorkerReaper(ctx context.Context, tick time.Duration, maxIdle time.Duration, idleCap int) {
	go func() {
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if n := m.ReapIdleWorkers(maxIdle, idleCap); n > 0 {
					slog.Info("reaped idle pi workers", "count", n)
				}
			}
		}
	}()
}
```

Add `sort` and `github.com/gsd-build/daemon/internal/pi` imports.

- [ ] **Step 6: Wire post-task reaping from the manager**

In `Manager.Spawn`, set the actor callback before `NewActor(opts)`:

```go
opts.OnTaskIdle = func() {
	m.ReapIdleWorkers(m.cfg.EffectiveWarmWorkerIdle(), m.cfg.EffectiveWarmWorkerIdleCap())
}
```

- [ ] **Step 7: Expose status**

Modify `internal/sockapi/provider.go`:

```go
type WorkerInfo struct {
	SessionID string     `json:"sessionID"`
	Provider  string     `json:"provider"`
	Model     string     `json:"model"`
	PID       int        `json:"pid"`
	KeyHash   string     `json:"keyHash"`
	State     string     `json:"state"`
	StartedAt time.Time  `json:"startedAt"`
	LastUsedAt time.Time  `json:"lastUsedAt"`
	IdleSince *time.Time `json:"idleSince"`
}

type StatusData struct {
	Version              string `json:"version"`
	Uptime               string `json:"uptime"`
	RelayConnected       bool   `json:"relayConnected"`
	RelayURL             string `json:"relayURL"`
	MachineID            string `json:"machineID"`
	ActiveSessions       int    `json:"activeSessions"`
	InFlightTasks        int    `json:"inFlightTasks"`
	MaxConcurrentTasks   int    `json:"maxConcurrentTasks"`
	WarmWorkerIdleTTL    string `json:"warmWorkerIdleTTL"`
	WarmWorkerIdleCap    int    `json:"warmWorkerIdleCap"`
	ActiveWarmWorkers    int    `json:"activeWarmWorkers"`
	IdleWarmWorkers      int    `json:"idleWarmWorkers"`
	LogLevel             string `json:"logLevel"`
}

type StatusProvider interface {
	Health() HealthData
	Status() StatusData
	Sessions() []SessionInfo
	Workers() []WorkerInfo
}
```

Add `GET /workers` in `internal/sockapi/handler.go`.

- [ ] **Step 8: Start worker reaper in daemon loop**

In `internal/loop/daemon.go`, near actor reaper startup:

```go
d.manager.StartReaper(ctx, 5*time.Minute, 30*time.Minute)
d.manager.StartWorkerReaper(ctx, time.Minute, d.cfg.EffectiveWarmWorkerIdle(), d.cfg.EffectiveWarmWorkerIdleCap())
```

Update daemon `Status()` and add `Workers()` to map `pi.WorkerSnapshot` to `sockapi.WorkerInfo`.

- [ ] **Step 9: Run manager and sockapi tests**

Run:

```bash
go test ./internal/session ./internal/sockapi ./internal/loop -count=1
```

Expected: PASS.

- [ ] **Step 10: Commit worker eviction and status**

```bash
git add internal/session/actor.go internal/session/manager.go internal/session/manager_test.go internal/sockapi/provider.go internal/sockapi/handler.go internal/sockapi/handler_test.go internal/loop/daemon.go
git commit -m "feat: bound warm pi worker lifetime"
```

## Task 6: Add Daemon Warm Worker Integration Coverage

**Files:**
- Modify: `tests/e2e/fixtures.go`
- Modify: `tests/e2e/daemon_e2e_test.go`
- Modify: `internal/session/manager_test.go`

- [ ] **Step 1: Add warm fake Pi fixture**

Modify `tests/e2e/fixtures.go` with a fixture that stays alive:

```go
func writeWarmFakePi(t *testing.T, destDir string) string {
	t.Helper()
	path := filepath.Join(destDir, "fake-pi-warm")
	body := `#!/bin/sh
count=0
while IFS= read -r line; do
  case "$line" in
    *'"type":"prompt"'*)
      count=$((count + 1))
      printf '%s\n' '{"type":"response","command":"prompt","success":true}'
      printf '%s\n' "{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_delta\",\"contentIndex\":0,\"delta\":\"warm-$count\"},\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"warm-$count\"}],\"api\":\"anthropic-messages\",\"provider\":\"claude-cli\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input\":0,\"output\":0,\"cacheRead\":0,\"cacheWrite\":0,\"totalTokens\":0,\"cost\":{\"total\":0}}}}"
      printf '%s\n' "{\"type\":\"agent_end\",\"messages\":[{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"warm-$count\"}],\"usage\":{\"input\":2,\"output\":3,\"cacheRead\":0,\"cacheWrite\":0,\"cost\":{\"total\":0.001}}}]}"
      ;;
  esac
done
`
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatalf("write warm fake pi: %v", err)
	}
	return path
}
```

- [ ] **Step 2: Write e2e reuse test**

Add to `tests/e2e/daemon_e2e_test.go`:

```go
func TestDaemonWarmPiWorkerReusesProcessAcrossTasks(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e integration test in short mode")
	}

	const (
		machineID = "test-machine-warm"
		authToken = "test-token-warm"
		sessionID = "test-session-warm"
		channelID = "ch-warm"
	)

	relay := NewStubRelay(t)
	home := makeTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("GSD_WARM_PI_WORKERS", "1")
	t.Setenv("GSD_PI_EXTENSION_PATH", writeFakePiExtension(t, home))
	fakePi := writeWarmFakePi(t, home)

	cfg := makeTestConfig(relay.URL(), machineID, authToken)
	cwd := t.TempDir()

	daemon, err := loop.NewWithPiBinaryPath(cfg, "test-version", fakePi)
	if err != nil {
		t.Fatalf("loop.NewWithPiBinaryPath: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- daemon.Run(ctx)
	}()

	if err := relay.WaitForConnection(5 * time.Second); err != nil {
		t.Fatalf("waiting for daemon connection: %v", err)
	}
	if _, err := relay.WaitForFrame(protocol.MsgTypeHello, 3*time.Second); err != nil {
		t.Fatalf("waiting for Hello: %v", err)
	}
	if err := relay.Send(&protocol.Welcome{Type: protocol.MsgTypeWelcome}); err != nil {
		t.Fatalf("send Welcome: %v", err)
	}

	for _, taskID := range []string{"task-warm-1", "task-warm-2"} {
		if err := relay.Send(&protocol.Task{
			Type: protocol.MsgTypeTask, TaskID: taskID, SessionID: sessionID,
			ChannelID: channelID, Prompt: taskID, CWD: cwd, Engine: "pi",
		}); err != nil {
			t.Fatalf("send Task %s: %v", taskID, err)
		}
		if _, err := relay.WaitForFrame(protocol.MsgTypeTaskStarted, 5*time.Second); err != nil {
			t.Fatalf("waiting for TaskStarted %s: %v", taskID, err)
		}
		if _, err := relay.WaitForFrame(protocol.MsgTypeTaskComplete, 15*time.Second); err != nil {
			t.Fatalf("waiting for TaskComplete %s: %v", taskID, err)
		}
	}

	workers := daemon.Workers()
	if len(workers) != 1 {
		t.Fatalf("workers = %d, want 1", len(workers))
	}
	if workers[0].State != "idle" {
		t.Fatalf("worker state = %q, want idle", workers[0].State)
	}

	cancel()
	select {
	case <-runErrCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("daemon did not shut down within 5s after cancel")
	}
}
```

- [ ] **Step 3: Run the e2e test**

Run:

```bash
go test ./tests/e2e -run TestDaemonWarmPiWorkerReusesProcessAcrossTasks -count=1
```

Expected: PASS.

- [ ] **Step 4: Run daemon package tests**

Run:

```bash
go test ./internal/session ./internal/pi ./internal/loop ./tests/e2e -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit integration coverage**

```bash
git add tests/e2e/fixtures.go tests/e2e/daemon_e2e_test.go internal/session/manager_test.go
git commit -m "test: cover warm pi worker reuse"
```

## Task 7: Harden Codex AppServer Warm Provider Verification

**Files:**
- Modify: `internal/pi/extension/codex-appserver-provider.test.mjs`
- Modify: `internal/pi/extension/codex-appserver-provider.ts`

- [ ] **Step 1: Add provider lifecycle test**

Extend `internal/pi/extension/codex-appserver-provider.test.mjs` with a fake AppServer test based on spike 018. The test should assert:

```js
assert.equal(summary.processStarts, 1);
assert.equal(summary.initializes, 1);
assert.equal(summary.threadStarts, 1);
assert.equal(summary.turnStarts, 2);
assert.deepEqual(summary.threadIds, ["thread_fake_warm"]);
```

The fake AppServer should emit `turn/started`, `item/started`, `item/agentMessage/delta`, `item/completed`, and `turn/completed` for each `turn/start`.

- [ ] **Step 2: Run provider test to verify behavior**

Run:

```bash
cd internal/pi/extension
npm test -- codex-appserver-provider.test.mjs
```

Expected: PASS if provider lifecycle already matches the spec. If the test fails because the provider restarts for the second turn, continue to Step 3.

- [ ] **Step 3: Keep AppServer state module-local**

In `internal/pi/extension/codex-appserver-provider.ts`, keep `codex`, `initialized`, `threadId`, `activeTurnId`, and request maps scoped inside `registerCodexAppServerProvider`. Ensure `ensureCodex()` returns immediately when `initialized && codex && !codex.killed`.

Use this shape:

```ts
async function ensureCodex(model: Model<any>, context: Context) {
  if (initialized && codex && !codex.killed) return;
  if (initPromise) return initPromise;
  initPromise = (async () => {
    codex = spawn(CODEX_BIN, ["app-server", "--listen", "stdio://"], {
      cwd: process.cwd(),
      stdio: ["pipe", "pipe", "pipe"],
    });
    rl = createInterface({ input: codex.stdout! });
    rl.on("line", handleCodexLine);
    codex.stderr?.on("data", () => {});
    codex.on("close", () => resetCodexState(new Error("Codex process exited")));
    await codexRequest("initialize", {
      clientInfo: { name: "gsd-daemon-pi-codex-provider", version: "0.0.1" },
      capabilities: { experimentalApi: true },
    });
    codexNotify("initialized");
    const result = await codexRequest("thread/start", {
      cwd: process.cwd(),
      sandbox: "read-only",
      approvalPolicy: "never",
      model: model.id,
      dynamicTools: codexDynamicToolsFromContext(context),
      ephemeral: true,
      experimentalRawEvents: false,
      persistExtendedHistory: false,
    });
    threadId = result.thread?.id;
    initialized = true;
  })();
  return initPromise;
}
```

- [ ] **Step 4: Run extension tests**

Run:

```bash
cd internal/pi/extension
npm test
```

Expected: PASS.

- [ ] **Step 5: Commit Codex verification**

```bash
git add internal/pi/extension/codex-appserver-provider.ts internal/pi/extension/codex-appserver-provider.test.mjs
git commit -m "test: verify codex appserver warm lifecycle"
```

## Task 8: Add Warm Claude SDK Worker

**Files:**
- Create: `internal/pi/extension/claude-sdk-worker.ts`
- Create: `internal/pi/extension/claude-sdk-worker.test.mjs`
- Modify: `internal/pi/extension/index.ts`

- [ ] **Step 1: Write fake Claude SDK worker test**

Create `internal/pi/extension/claude-sdk-worker.test.mjs`:

```js
import assert from "node:assert/strict";
import test from "node:test";
import { createWarmClaudeSdkWorkerForTest } from "./claude-sdk-worker.ts";

test("warm Claude SDK worker keeps one query pump across two turns", async () => {
  const worker = createWarmClaudeSdkWorkerForTest({
    async *query({ prompt }) {
      let count = 0;
      for await (const _msg of prompt) {
        count += 1;
        yield {
          type: "stream_event",
          event: { type: "content_block_start", content_block: { type: "text" } },
        };
        yield {
          type: "stream_event",
          event: { type: "content_block_delta", delta: { type: "text_delta", text: `turn-${count}` } },
        };
        yield {
          type: "stream_event",
          event: { type: "content_block_stop" },
        };
        yield {
          type: "result",
          subtype: "success",
          duration_ms: 1,
          duration_api_ms: 1,
          is_error: false,
          num_turns: count,
          session_id: "fake-session",
          total_cost_usd: 0,
          usage: { input_tokens: count, output_tokens: count },
        };
      }
    },
  });

  const seen = [];
  const first = await worker.turn({
    messages: [{
      type: "user",
      message: { role: "user", content: "first" },
      parent_tool_use_id: null,
      session_id: "fake-session",
    }],
    onMessage(message) {
      if (message.type === "stream_event" && message.event?.type === "content_block_delta") {
        seen.push(message.event.delta.text);
      }
    },
  });
  const second = await worker.turn({
    messages: [{
      type: "user",
      message: { role: "user", content: "second" },
      parent_tool_use_id: null,
      session_id: "fake-session",
    }],
    onMessage(message) {
      if (message.type === "stream_event" && message.event?.type === "content_block_delta") {
        seen.push(message.event.delta.text);
      }
    },
  });
  await worker.stop();

  assert.equal(first.result?.type, "result");
  assert.equal(second.result?.type, "result");
  assert.deepEqual(seen, ["turn-1", "turn-2"]);
  assert.equal(worker.queryStartsForTest(), 1);
});
```

- [ ] **Step 2: Run test to verify failure**

Run:

```bash
cd internal/pi/extension
npm test -- claude-sdk-worker.test.mjs
```

Expected: FAIL because `claude-sdk-worker.ts` is missing.

- [ ] **Step 3: Implement Claude SDK worker**

Create `internal/pi/extension/claude-sdk-worker.ts`:

```ts
import type { SDKMessage, SDKUserMessage, SDKUserMessageReplay } from "@anthropic-ai/claude-agent-sdk";

type SdkPromptInputMessage = SDKUserMessage | SDKUserMessageReplay;
type QueryFactory = (args: { prompt: AsyncIterable<SdkPromptInputMessage>; options?: any }) => AsyncIterable<SDKMessage>;

type TurnRequest = {
  messages: SdkPromptInputMessage[];
  onMessage: (message: SDKMessage) => void;
};

type TurnResult = {
  result: SDKMessage | null;
};

class AsyncPromptQueue<T> implements AsyncIterable<T> {
  private values: T[] = [];
  private waiters: ((value: IteratorResult<T>) => void)[] = [];
  private closed = false;

  push(value: T) {
    if (this.closed) throw new Error("Claude SDK prompt queue is closed.");
    const waiter = this.waiters.shift();
    if (waiter) waiter({ value, done: false });
    else this.values.push(value);
  }

  close() {
    this.closed = true;
    for (const waiter of this.waiters.splice(0)) {
      waiter({ value: undefined, done: true });
    }
  }

  [Symbol.asyncIterator]() {
    return {
      next: () => {
        if (this.values.length > 0) {
          return Promise.resolve({ value: this.values.shift(), done: false });
        }
        if (this.closed) {
          return Promise.resolve({ value: undefined, done: true });
        }
        return new Promise<IteratorResult<any>>((resolve) => this.waiters.push(resolve));
      },
    };
  }
}

export class WarmClaudeSdkWorker {
  private prompt = new AsyncPromptQueue<SdkPromptInputMessage>();
  private queryStarted = 0;
  private active: {
    resolve: (result: TurnResult) => void;
    reject: (error: Error) => void;
    onMessage: (message: SDKMessage) => void;
  } | null = null;
  private pumpStarted = false;
  private pumpError: Error | null = null;

  constructor(private readonly queryFactory: QueryFactory, private readonly optionsFactory: () => any = () => ({})) {}

  queryStartsForTest() {
    return this.queryStarted;
  }

  hasStarted() {
    return this.queryStarted > 0;
  }

  async turn(request: TurnRequest): Promise<TurnResult> {
    if (this.active) {
      throw new Error("Claude SDK worker already has an active turn.");
    }
    if (this.pumpError) {
      throw this.pumpError;
    }
    this.ensurePump();
    const result = new Promise<TurnResult>((resolve, reject) => {
      this.active = { resolve, reject, onMessage: request.onMessage };
    });
    for (const message of request.messages) this.prompt.push(message);
    return result;
  }

  async stop() {
    const err = new Error("WarmClaudeSdkWorker stopped");
    this.pumpError = err;
    if (this.active) {
      const active = this.active;
      this.active = null;
      active.reject(err);
    }
    this.prompt.close();
  }

  private ensurePump() {
    if (this.pumpStarted) return;
    this.pumpStarted = true;
    this.queryStarted += 1;
    void this.pump();
  }

  private async pump() {
    try {
      for await (const msg of this.queryFactory({ prompt: this.prompt, options: this.optionsFactory() })) {
        this.active?.onMessage(msg);
        if (msg.type === "result") {
          const active = this.active;
          this.active = null;
          active?.resolve({ result: msg });
        }
      }
    } catch (error) {
      const err = error instanceof Error ? error : new Error(String(error));
      this.pumpError = err;
      const active = this.active;
      this.active = null;
      active?.reject(err);
    }
  }
}

export function createWarmClaudeSdkWorkerForTest(args: { query: QueryFactory }) {
  return new WarmClaudeSdkWorker(args.query);
}
```

- [ ] **Step 4: Run worker test**

Run:

```bash
cd internal/pi/extension
npm test -- claude-sdk-worker.test.mjs
```

Expected: PASS.

- [ ] **Step 5: Wire worker behind feature flag**

In `internal/pi/extension/index.ts`, import the worker:

```ts
import { WarmClaudeSdkWorker } from "./claude-sdk-worker.js";
```

Add module-level state:

```ts
let warmClaudeWorker: WarmClaudeSdkWorker | null = null;
```

Add a small option key so the query pump restarts when the model, system prompt, or tool surface changes:

```ts
function warmClaudeOptionsKey(model: Model<any>, context: Context) {
  const tools = ((context.tools as PiTool[] | undefined) ?? []).map((tool) => tool.name).sort();
  return JSON.stringify({
    model: model.id,
    systemPrompt: context.systemPrompt ?? "",
    tools,
    browserGrant: browserGrantFromEnv() ?? null,
  });
}

let warmClaudeOptionsSignature = "";

function resetWarmClaudeWorker() {
  void warmClaudeWorker?.stop();
  warmClaudeWorker = null;
  warmClaudeOptionsSignature = "";
}
```

At the top of `streamClaudeSdk`, route through the warm worker only when enabled:

```ts
if (process.env.GSD_WARM_CLAUDE_SDK === "1") {
  return streamWarmClaudeSdk(model, context, options);
}
```

Extract the SDK-message event handling from `streamClaudeSdk` into this helper so cold and warm paths share the same Pi event contract:

```ts
function createClaudeSdkRunHandlers(
  stream: AssistantMessageEventStream,
  output: AssistantMessage,
  model: Model<any>,
  options?: SimpleStreamOptions,
) {
  let activeTextIndex: number | null = null;
  let activeToolCall: ActiveToolCall | null = null;
  let streamEndedForToolUse = false;
  let streamClosed = false;

  const closeStreamForToolUse = () => {
    if (streamClosed) return;
    stream.push({ type: "done", reason: "toolUse", message: output });
    stream.end();
    streamClosed = true;
  };

  const handleMessage = (msg: SDKMessage) => {
    applyUsageFromSdkMessage(output.usage, msg, (model as any).cost);
    if (streamEndedForToolUse) return;
    if (msg.type !== "stream_event") return;
    const ev = (msg as any).event;
    if (ev?.type === "content_block_start") {
      const block = ev.content_block;
      if (block?.type === "text") {
        output.content.push({ type: "text", text: "" });
        activeTextIndex = output.content.length - 1;
        stream.push({ type: "text_start", contentIndex: activeTextIndex, partial: output });
      } else if (block?.type === "tool_use") {
        const fullName = block.name as string;
        const piName = fullName.startsWith(MCP_PREFIX) ? fullName.slice(MCP_PREFIX.length) : fullName;
        const id = block.id as string;
        output.content.push({ type: "toolCall", id, name: piName, arguments: {} });
        activeToolCall = { idx: output.content.length - 1, id, name: piName, jsonAcc: "" };
        stream.push({ type: "toolcall_start", contentIndex: activeToolCall.idx, partial: output });
      }
    } else if (ev?.type === "content_block_delta") {
      const delta = ev.delta;
      if (delta?.type === "text_delta" && activeTextIndex !== null) {
        const text = delta.text as string;
        const blk = output.content[activeTextIndex] as any;
        blk.text += text;
        stream.push({ type: "text_delta", contentIndex: activeTextIndex, delta: text, partial: output });
      } else if (delta?.type === "input_json_delta" && activeToolCall) {
        const chunk = delta.partial_json as string;
        activeToolCall.jsonAcc += chunk;
        try {
          const parsed = JSON.parse(activeToolCall.jsonAcc);
          (output.content[activeToolCall.idx] as any).arguments = parsed;
        } catch {}
        stream.push({ type: "toolcall_delta", contentIndex: activeToolCall.idx, delta: chunk, partial: output });
      }
    } else if (ev?.type === "content_block_stop") {
      if (activeTextIndex !== null) {
        const finalText = (output.content[activeTextIndex] as any).text;
        stream.push({ type: "text_end", contentIndex: activeTextIndex, content: finalText, partial: output });
        activeTextIndex = null;
      }
      if (activeToolCall) {
        const blk = output.content[activeToolCall.idx] as any;
        const toolCall = finalizeActivePiToolCall(activeToolCall, blk.arguments);
        if (toolCall) {
          blk.arguments = toolCall.arguments;
          stream.push({ type: "toolcall_end", contentIndex: activeToolCall.idx, toolCall, partial: output });
          activeToolCall = null;
          output.stopReason = "toolUse";
          ensureNonZeroUsageForAbortedToolTurn(output.usage, output.content, (model as any).cost);
          streamEndedForToolUse = true;
        }
      }
    }
  };

  const finish = () => {
    if (streamEndedForToolUse) {
      closeStreamForToolUse();
      return;
    }
    output.stopReason = "stop";
    stream.push({ type: "done", reason: "stop", message: output });
    stream.end();
  };

  const fail = (err: unknown) => {
    if (streamEndedForToolUse) {
      closeStreamForToolUse();
      return;
    }
    output.stopReason = options?.signal?.aborted ? "aborted" : "error";
    output.errorMessage = err instanceof Error ? err.message : String(err);
    stream.push({ type: "error", reason: output.stopReason as any, error: output });
    stream.end();
  };

  return { handleMessage, finish, fail };
}
```

Then add `streamWarmClaudeSdk` below `streamClaudeSdk`:

```ts
function streamWarmClaudeSdk(
  model: Model<any>,
  context: Context,
  options?: SimpleStreamOptions,
): AssistantMessageEventStream {
  const stream = createAssistantMessageEventStream();
  const output: AssistantMessage = {
    role: "assistant",
    content: [],
    api: model.api,
    provider: model.provider,
    model: model.id,
    usage: {
      input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0,
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
    },
    stopReason: "stop",
    timestamp: Date.now(),
  };
  stream.push({ type: "start", partial: output });

  const browserGrant = browserGrantFromEnv();
  const piTools = mergeClaudeCliTools(context.tools as PiTool[] | undefined, browserGrant);
  const sdkTools = piTools.map((t) => piToolToSdkTool(t));
  const allowedTools = sdkTools.map((t: any) => `${MCP_PREFIX}${(t as any).name}`);
  const piMcp = createSdkMcpServer({ name: "pi-tools", version: "0.0.1", tools: sdkTools });
  const sdkAbort = new AbortController();
  options?.signal?.addEventListener("abort", () => sdkAbort.abort(), { once: true });

  const sdkOptions: SdkOptions = {
    includePartialMessages: true,
    persistSession: false,
    settingSources: [],
    allowedTools,
    disallowedTools: CLAUDE_BUILTINS,
    mcpServers: { "pi-tools": piMcp },
    abortController: sdkAbort,
    permissionMode: "bypassPermissions",
    stderr: () => {},
  };
  if (context.systemPrompt) sdkOptions.systemPrompt = context.systemPrompt;

  const signature = warmClaudeOptionsKey(model, context);
  if (warmClaudeOptionsSignature && warmClaudeOptionsSignature !== signature) {
    resetWarmClaudeWorker();
  }
  if (!warmClaudeWorker) {
    warmClaudeOptionsSignature = signature;
    warmClaudeWorker = new WarmClaudeSdkWorker(query, () => sdkOptions);
  }

  const handlers = createClaudeSdkRunHandlers(stream, output, model, options);
  const sdkSessionId = deriveClaudeSdkSessionId(undefined, process.cwd());
  const replayHistory = context.messages.length > 1 && !warmClaudeWorker.hasStarted();
  const messages = buildClaudePromptMessages(context.messages, sdkSessionId, replayHistory);

  warmClaudeWorker.turn({ messages, onMessage: handlers.handleMessage })
    .then(() => handlers.finish())
    .catch((err) => {
      resetWarmClaudeWorker();
      handlers.fail(err);
    });

  return stream;
}
```

- [ ] **Step 6: Run extension tests**

Run:

```bash
cd internal/pi/extension
npm test
```

Expected: PASS.

- [ ] **Step 7: Commit warm Claude SDK worker**

```bash
git add internal/pi/extension/index.ts internal/pi/extension/claude-sdk-worker.ts internal/pi/extension/claude-sdk-worker.test.mjs
git commit -m "feat: add warm claude sdk worker"
```

## Task 9: Run Final Verification

**Files:**
- No planned source edits.

- [ ] **Step 1: Run Go test suite**

Run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 2: Run extension test suite**

Run:

```bash
cd internal/pi/extension
npm test
```

Expected: PASS.

- [ ] **Step 3: Build daemon**

Run:

```bash
go build -o gsd-cloud .
```

Expected: PASS and binary created at `./gsd-cloud`.

- [ ] **Step 4: Inspect process cleanup manually with fake Pi**

Run:

```bash
GSD_WARM_PI_WORKERS=1 go test ./tests/e2e -run TestDaemonWarmPiWorkerReusesProcessAcrossTasks -count=1
ps -axo pid,ppid,stat,command | rg 'fake-pi-warm|pi -p --mode rpc' || true
```

Expected: test passes and no fake Pi process remains after test cleanup.

- [ ] **Step 5: Confirm clean task branch**

Run:

```bash
git status --short
git log --oneline --max-count=12
```

Expected: `git status --short` is empty and the task branch contains the warm-worker implementation commits.

## Execution Order

1. Task 1: Config
2. Task 2: Worker key
3. Task 3: Pi worker
4. Task 4: Actor integration
5. Task 5: Eviction and status
6. Task 6: Integration coverage
7. Task 7: Codex lifecycle verification
8. Task 8: Claude SDK worker
9. Task 9: Final verification

This order keeps every commit independently testable and keeps provider-specific warmth behind the safe daemon worker boundary.
