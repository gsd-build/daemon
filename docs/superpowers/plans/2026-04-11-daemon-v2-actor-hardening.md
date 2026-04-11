# Daemon v2: Actor Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Harden the daemon's session/actor subsystem with concurrency limits, per-task timeouts, child process lifecycle management, and an actor reaper to prevent goroutine leaks.

**Architecture:** The `internal/config` package gains `MaxConcurrentTasks` and `TaskTimeoutMinutes` fields. The `Manager` enforces concurrency limits at spawn time, starts a reaper goroutine, and tracks in-flight task count. The `Actor` gains a `lastActiveAt` timestamp updated on task completion and a timeout-based context for each task. The `Executor` sets `Setpgid: true` on child processes and writes PID files. A `pidfile` package handles PID file I/O and stale cleanup.

**Tech Stack:** Go 1.25, `github.com/gsd-build/protocol-go` v0.4.0

**Spec reference:** `docs/superpowers/specs/2026-04-11-daemon-v2-design.md` Section 4 (Session & Actor Architecture)

---

## File Structure

| File | Responsibility |
|---|---|
| Modify: `internal/config/config.go` | Add `MaxConcurrentTasks`, `TaskTimeoutMinutes` fields with defaults |
| Create: `internal/config/config_test.go` | Tests for new config fields and defaults |
| Create: `internal/pidfile/pidfile.go` | PID file write, read, remove, stale cleanup |
| Create: `internal/pidfile/pidfile_test.go` | Tests for PID file lifecycle |
| Modify: `internal/claude/executor.go` | Set `SysProcAttr.Setpgid = true`, accept PID callback |
| Create: `internal/claude/executor_unix_test.go` | Test for process group and PID callback |
| Modify: `internal/session/actor.go` | Add `lastActiveAt` field, per-task timeout, SIGTERM→SIGKILL escalation |
| Modify: `internal/session/actor_test.go` | Tests for timeout and lastActiveAt |
| Modify: `internal/session/manager.go` | Concurrency check, memory check, reaper goroutine, in-flight tracking |
| Create: `internal/session/manager_test.go` | Tests for concurrency limit, memory rejection, reaper |

---

### Task 1: Add Effective* methods for concurrency and timeout

**Prerequisite:** Plan 2 (Service Installer) Task 1 already adds `MaxConcurrentTasks`, `TaskTimeoutMinutes`, and `LogLevel` fields to `internal/config/config.go`, along with `LoadFrom` and `SaveTo` methods. This task only adds the `Effective*` convenience methods and their tests.

**Files:**
- Modify: `internal/config/config.go` (add methods only — fields already exist from Plan 2)
- Modify: `internal/config/config_test.go` (add test functions — file already exists from Plan 2)

- [ ] **Step 1: Write the failing tests for Effective* methods**

Append to `internal/config/config_test.go`:

```go
func TestEffectiveMaxConcurrentTasks(t *testing.T) {
	cfg := &Config{}
	got := cfg.EffectiveMaxConcurrentTasks()
	if got < 1 {
		t.Errorf("expected at least 1, got %d", got)
	}

	cfg.MaxConcurrentTasks = 4
	if cfg.EffectiveMaxConcurrentTasks() != 4 {
		t.Errorf("expected 4, got %d", cfg.EffectiveMaxConcurrentTasks())
	}
}

func TestEffectiveTaskTimeout(t *testing.T) {
	cfg := &Config{}
	if cfg.EffectiveTaskTimeout().Minutes() != 30 {
		t.Errorf("expected 30m default, got %v", cfg.EffectiveTaskTimeout())
	}

	cfg.TaskTimeoutMinutes = 60
	if cfg.EffectiveTaskTimeout().Minutes() != 60 {
		t.Errorf("expected 60m, got %v", cfg.EffectiveTaskTimeout())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/config/ -run TestEffective -v
```

Expected: FAIL — `EffectiveMaxConcurrentTasks`, `EffectiveTaskTimeout` not defined.

- [ ] **Step 3: Implement Effective* methods**

Append to `internal/config/config.go`:

```go
// EffectiveMaxConcurrentTasks returns the configured limit or runtime.NumCPU()
// when the field is 0 (unset).
func (c *Config) EffectiveMaxConcurrentTasks() int {
	if c.MaxConcurrentTasks > 0 {
		return c.MaxConcurrentTasks
	}
	return runtime.NumCPU()
}

// EffectiveTaskTimeout returns the configured timeout duration or the 30-minute
// default when the field is 0 (unset).
func (c *Config) EffectiveTaskTimeout() time.Duration {
	if c.TaskTimeoutMinutes > 0 {
		return time.Duration(c.TaskTimeoutMinutes) * time.Minute
	}
	return 30 * time.Minute
}
```

Add `"runtime"` and `"time"` to the import block if not already present.

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/config/ -run TestEffective -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add EffectiveMaxConcurrentTasks and EffectiveTaskTimeout

Convenience methods that return configured values or sensible defaults
(runtime.NumCPU for concurrency, 30 minutes for timeout)."
```

---

### Task 2: Create PID file package

**Files:**
- Create: `internal/pidfile/pidfile.go`
- Create: `internal/pidfile/pidfile_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/pidfile/pidfile_test.go
package pidfile

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestWriteAndRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.pid")

	if err := Write(path, 12345); err != nil {
		t.Fatalf("write: %v", err)
	}

	pid, err := Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if pid != 12345 {
		t.Errorf("expected 12345, got %d", pid)
	}
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.pid")

	if err := Write(path, 99); err != nil {
		t.Fatal(err)
	}
	Remove(path)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected file to be removed")
	}
}

func TestReadMissingFile(t *testing.T) {
	_, err := Read("/nonexistent/path/test.pid")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestCleanStale(t *testing.T) {
	dir := t.TempDir()

	// Write a PID file with PID 0 (guaranteed not running)
	path := filepath.Join(dir, "fake.pid")
	if err := os.WriteFile(path, []byte("0"), 0600); err != nil {
		t.Fatal(err)
	}

	// Write a PID file with our own PID (guaranteed running)
	selfPath := filepath.Join(dir, "self.pid")
	if err := os.WriteFile(selfPath, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}

	cleaned := CleanStale(dir)

	// PID 0 file should be removed
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected stale PID file to be removed")
	}

	// Our own PID file should remain
	if _, err := os.Stat(selfPath); err != nil {
		t.Error("expected self PID file to remain")
	}

	if cleaned != 1 {
		t.Errorf("expected 1 cleaned, got %d", cleaned)
	}
}

func TestPidDir(t *testing.T) {
	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if dir == "" {
		t.Error("expected non-empty dir")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/pidfile/ -v
```

Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement pidfile package**

```go
// internal/pidfile/pidfile.go
package pidfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Dir returns the PID file directory: ~/.gsd-cloud/pids/
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home: %w", err)
	}
	return filepath.Join(home, ".gsd-cloud", "pids"), nil
}

// Write creates a PID file at the given path.
func Write(path string, pid int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return os.WriteFile(path, []byte(strconv.Itoa(pid)), 0600)
}

// Read returns the PID stored in the file.
func Read(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parse pid: %w", err)
	}
	return pid, nil
}

// Remove deletes the PID file. No error if it doesn't exist.
func Remove(path string) {
	_ = os.Remove(path)
}

// CleanStale removes PID files in dir whose process is no longer running.
// Returns the number of files cleaned.
func CleanStale(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}

	cleaned := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		pid, err := Read(path)
		if err != nil {
			Remove(path)
			cleaned++
			continue
		}
		if !processRunning(pid) {
			Remove(path)
			cleaned++
		}
	}
	return cleaned
}

// processRunning checks if a process with the given PID exists.
// Sends signal 0 which doesn't affect the process but validates the PID.
func processRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/pidfile/ -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/pidfile/pidfile.go internal/pidfile/pidfile_test.go
git commit -m "feat(pidfile): add PID file write/read/remove and stale cleanup

Writes PID files to ~/.gsd-cloud/pids/. CleanStale removes files
whose process is no longer running (signal 0 check)."
```

---

### Task 3: Add process group and PID callback to executor

**Files:**
- Modify: `internal/claude/executor.go`
- Create: `internal/claude/executor_unix_test.go`

- [ ] **Step 1: Write the failing test for process group and PID callback**

```go
// internal/claude/executor_unix_test.go
//go:build !windows

package claude

import (
	"context"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestExecutorSetsProcessGroup(t *testing.T) {
	// Use a simple long-running command to verify Setpgid is set
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		// Kill the process group
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()

	// Verify the process has its own process group
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("getpgid: %v", err)
	}
	if pgid != cmd.Process.Pid {
		t.Errorf("expected pgid=%d (own group), got pgid=%d", cmd.Process.Pid, pgid)
	}
}

func TestPIDCallbackInvoked(t *testing.T) {
	// Create a fake binary that exits immediately
	tmp := t.TempDir()
	script := tmp + "/fake.sh"
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho '{\"type\":\"result\",\"session_id\":\"s1\",\"total_cost_usd\":0}'\n"), 0755); err != nil {
		t.Fatal(err)
	}

	var captured atomic.Int64

	exec := NewExecutor(Options{
		BinaryPath: script,
		CWD:        tmp,
		Prompt:     "test",
	})
	exec.OnPIDStart = func(pid int) {
		captured.Store(int64(pid))
	}
	exec.OnPIDExit = func(pid int) {}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = exec.Run(ctx, func(e Event) error { return nil })

	if captured.Load() == 0 {
		t.Error("expected PID callback to be invoked with non-zero PID")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/claude/ -run TestPIDCallbackInvoked -v
```

Expected: FAIL — `OnPIDStart` field not defined on Executor.

- [ ] **Step 3: Add process group and PID callbacks to executor**

Replace the `Executor` struct and the `Run` method in `internal/claude/executor.go`:

Add to the `Executor` struct (after `opts Options`):

```go
// Executor spawns a single `claude -p` process and reads its output.
type Executor struct {
	opts Options

	// OnPIDStart is called with the child PID after a successful Start().
	// Set by the actor for PID file tracking. May be nil.
	OnPIDStart func(pid int)

	// OnPIDExit is called with the child PID after the process exits.
	// Set by the actor for PID file cleanup. May be nil.
	OnPIDExit func(pid int)
}
```

In the `Run` method, after `cmd := exec.CommandContext(...)`, add the `SysProcAttr` and after `cmd.Start()`, add the PID callbacks:

```go
	cmd := exec.CommandContext(ctx, e.opts.BinaryPath, args...)
	cmd.Dir = e.opts.CWD
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if len(e.opts.Env) > 0 {
		cmd.Env = append(os.Environ(), e.opts.Env...)
	}
```

After the successful `cmd.Start()`:

```go
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("start: %w", err)
	}

	pid := cmd.Process.Pid
	if e.OnPIDStart != nil {
		e.OnPIDStart(pid)
	}
	defer func() {
		if e.OnPIDExit != nil {
			e.OnPIDExit(pid)
		}
	}()
```

Add `"syscall"` to the import block.

The full updated `internal/claude/executor.go`:

```go
//go:build !windows

package claude

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// Options configures a Claude process.
type Options struct {
	BinaryPath     string
	CWD            string
	Model          string
	Effort         string
	PermissionMode string
	SystemPrompt   string
	ResumeSession  string   // claude session id to resume; empty = new session
	AllowedTools   []string // tools to pass via --allowedTools
	Env            []string // extra environment variables; nil = inherit
	Prompt         string   // user's message text
}

// Executor spawns a single `claude -p` process and reads its output.
type Executor struct {
	opts Options

	// OnPIDStart is called with the child PID after a successful Start().
	// Set by the actor for PID file tracking. May be nil.
	OnPIDStart func(pid int)

	// OnPIDExit is called with the child PID after the process exits.
	// Set by the actor for PID file cleanup. May be nil.
	OnPIDExit func(pid int)
}

// NewExecutor constructs an Executor. Call Run to spawn the process.
func NewExecutor(opts Options) *Executor {
	if opts.BinaryPath == "" {
		opts.BinaryPath = "claude"
	}
	return &Executor{opts: opts}
}

// Run spawns the claude process with the prompt as a CLI argument,
// parses stream-json events from stdout, and blocks until the process
// exits or ctx is canceled. Each event is delivered via onEvent.
//
// Stdout is a regular pipe. Claude CLI (Node.js) block-buffers the
// first ~4KB then flushes per-line. The initial batch delay is
// imperceptible against Claude's multi-second response time.
//
// Stderr is captured in a bounded ring buffer so crashes, auth errors,
// and rate-limit messages surface in the returned error.
func (e *Executor) Run(ctx context.Context, onEvent func(Event) error) error {
	log.Printf("[executor] starting claude: binary=%s dir=%s model=%q prompt=%q",
		e.opts.BinaryPath, e.opts.CWD, e.opts.Model, truncateStr(e.opts.Prompt, 80))

	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
	}
	if e.opts.Model != "" {
		args = append(args, "--model", e.opts.Model)
	}
	if e.opts.Effort != "" {
		args = append(args, "--effort", e.opts.Effort)
	}
	if e.opts.PermissionMode != "" {
		args = append(args, "--permission-mode", e.opts.PermissionMode)
	}
	if e.opts.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", e.opts.SystemPrompt)
	}
	if e.opts.ResumeSession != "" {
		args = append(args, "--resume", e.opts.ResumeSession)
	}
	if len(e.opts.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(e.opts.AllowedTools, ","))
	}
	// "--" stops flag parsing so the prompt is never consumed by variadic flags
	args = append(args, "--", e.opts.Prompt)

	cmd := exec.CommandContext(ctx, e.opts.BinaryPath, args...)
	cmd.Dir = e.opts.CWD
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if len(e.opts.Env) > 0 {
		cmd.Env = append(os.Environ(), e.opts.Env...)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	stderrBuf := newStderrBuffer(50, 16*1024)

	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return nil // context was cancelled before or during start
		}
		return fmt.Errorf("start: %w", err)
	}

	pid := cmd.Process.Pid
	if e.OnPIDStart != nil {
		e.OnPIDStart(pid)
	}
	defer func() {
		if e.OnPIDExit != nil {
			e.OnPIDExit(pid)
		}
	}()

	// Drain stderr in background
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		stderrBuf.drain(stderr)
	}()

	parseErr := Parse(stdout, onEvent)
	waitErr := cmd.Wait()
	<-stderrDone

	if waitErr != nil {
		if ctx.Err() != nil {
			return nil // expected shutdown via context cancellation
		}
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			code := exitErr.ExitCode()
			if code > 0 {
				if tail := stderrBuf.String(); tail != "" {
					return fmt.Errorf("claude exited with code %d: %s", code, tail)
				}
				return fmt.Errorf("claude exited with code %d (no stderr)", code)
			}
		} else {
			return fmt.Errorf("claude wait: %w", waitErr)
		}
	}
	if parseErr != nil && parseErr != io.EOF {
		if tail := stderrBuf.String(); tail != "" {
			return fmt.Errorf("%w (claude stderr: %s)", parseErr, tail)
		}
		return parseErr
	}
	return nil
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/claude/ -run "TestExecutorSetsProcessGroup|TestPIDCallbackInvoked" -v
```

Expected: PASS.

- [ ] **Step 5: Run existing executor tests to confirm no regressions**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/claude/ -v
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/claude/executor.go internal/claude/executor_unix_test.go
git commit -m "feat(executor): set Setpgid on child processes, add PID callbacks

Child processes get their own process group for clean group-kill on
shutdown. OnPIDStart/OnPIDExit callbacks enable PID file tracking."
```

---

### Task 4: Add lastActiveAt and per-task timeout to Actor

**Files:**
- Modify: `internal/session/actor.go`
- Modify: `internal/session/actor_test.go`

- [ ] **Step 1: Write the failing test for per-task timeout**

Append to `internal/session/actor_test.go`:

```go
func TestActorTaskTimeout(t *testing.T) {
	binPath := buildFakeClaude(t)
	relay := newFakeRelay()

	// FAKE_CLAUDE_SLEEP makes fake-claude sleep for N seconds before producing output
	t.Setenv("FAKE_CLAUDE_SLEEP", "10")

	actor, err := NewActor(Options{
		SessionID:  "sess-timeout",
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Relay:      relay,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	defer actor.Stop()

	// Set a very short timeout for testing
	actor.taskTimeout = 1 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- actor.Run(ctx) }()

	if err := actor.SendTask(protocol.Task{
		TaskID:    "t-timeout",
		SessionID: "sess-timeout",
		ChannelID: "ch1",
		Prompt:    "slow task",
	}); err != nil {
		t.Fatal(err)
	}

	// Should get a TaskError with timeout message
	gotError := relay.waitFor(t, 10*time.Second, func(frames []any) bool {
		for _, f := range frames {
			if te, ok := f.(*protocol.TaskError); ok {
				if te.TaskID == "t-timeout" {
					return true
				}
			}
		}
		return false
	})
	if !gotError {
		t.Fatal("expected TaskError for timeout")
	}

	// Verify the actor is still alive
	select {
	case err := <-done:
		t.Fatalf("actor.Run() should not have returned, got: %v", err)
	default:
		// good
	}
}

func TestActorLastActiveAt(t *testing.T) {
	binPath := buildFakeClaude(t)
	relay := newFakeRelay()

	actor, err := NewActor(Options{
		SessionID:  "sess-active",
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Relay:      relay,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer actor.Stop()

	// LastActiveAt should be set to creation time
	initial := actor.LastActiveAt()
	if initial.IsZero() {
		t.Error("expected non-zero initial lastActiveAt")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = actor.Run(ctx) }()

	if err := actor.SendTask(protocol.Task{
		TaskID:    "t1",
		SessionID: "sess-active",
		ChannelID: "ch1",
		Prompt:    "hello",
	}); err != nil {
		t.Fatal(err)
	}

	if !relay.waitForTaskComplete(t, 10*time.Second) {
		t.Fatal("timed out waiting for TaskComplete")
	}

	updated := actor.LastActiveAt()
	if !updated.After(initial) {
		t.Errorf("expected lastActiveAt to advance: initial=%v updated=%v", initial, updated)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/session/ -run "TestActorTaskTimeout|TestActorLastActiveAt" -v
```

Expected: FAIL — `taskTimeout` field and `LastActiveAt` method not defined.

- [ ] **Step 3: Add lastActiveAt field and per-task timeout to Actor**

In `internal/session/actor.go`, add these fields to the `Actor` struct:

```go
// Actor drives a single Claude session using spawn-per-task execution.
// Each incoming task spawns a fresh claude process; no processes remain
// alive between tasks.
type Actor struct {
	opts Options

	seq       int64 // monotonic sequence counter, only touched by Run goroutine
	verbosity display.VerbosityLevel
	stream    *display.StreamHandler

	claudeSessionID string   // set from result events, used for --resume
	allowedTools    []string // accumulates as user grants permissions

	taskCh chan protocol.Task // SendTask writes here, Run reads
	permCh chan permResponse  // HandlePermissionResponse writes here

	// Question responses are routed by requestId so batch questions can
	// be answered in any order without blocking each other.
	questionMu sync.Mutex
	questionCh map[string]chan string // requestId → answer channel

	stopCh chan struct{}

	taskMu     sync.Mutex
	taskCancel context.CancelFunc // cancels the in-flight task context; nil when idle
	taskID     string             // ID of the in-flight task; empty when idle

	// taskTimeout is the per-task deadline. Zero means no timeout.
	// Set by the Manager from config before calling Run.
	taskTimeout time.Duration

	// lastActiveAt tracks when this actor last completed or received a task.
	// Protected by taskMu. Used by the reaper to detect idle actors.
	lastActiveAt time.Time
}
```

Update `NewActor` to initialize `lastActiveAt`:

```go
func NewActor(opts Options) (*Actor, error) {
	return &Actor{
		opts:            opts,
		verbosity:       opts.Verbosity,
		stream:          display.NewStreamHandler(os.Stdout, opts.Verbosity),
		claudeSessionID: opts.ResumeSession,
		taskCh:          make(chan protocol.Task, 1),
		permCh:          make(chan permResponse, 1),
		questionCh:      make(map[string]chan string),
		stopCh:          make(chan struct{}),
		lastActiveAt:    time.Now(),
	}, nil
}
```

Add the `LastActiveAt` method:

```go
// LastActiveAt returns the time of the actor's last task completion or creation.
func (a *Actor) LastActiveAt() time.Time {
	a.taskMu.Lock()
	defer a.taskMu.Unlock()
	return a.lastActiveAt
}
```

Add a `HasInFlightTask` method for the manager's concurrency tracking:

```go
// HasInFlightTask returns true if the actor is currently executing a task.
func (a *Actor) HasInFlightTask() bool {
	a.taskMu.Lock()
	defer a.taskMu.Unlock()
	return a.taskID != ""
}
```

Update `executeTask` to apply the timeout and update `lastActiveAt`:

```go
func (a *Actor) executeTask(ctx context.Context, task protocol.Task) error {
	var taskCtx context.Context
	var cancel context.CancelFunc

	if a.taskTimeout > 0 {
		taskCtx, cancel = context.WithTimeout(ctx, a.taskTimeout)
	} else {
		taskCtx, cancel = context.WithCancel(ctx)
	}

	a.taskMu.Lock()
	a.taskCancel = cancel
	a.taskID = task.TaskID
	a.taskMu.Unlock()

	defer func() {
		cancel()
		a.taskMu.Lock()
		a.taskCancel = nil
		a.taskID = ""
		a.lastActiveAt = time.Now()
		a.taskMu.Unlock()
	}()

	tc := &taskContext{
		TaskID:         task.TaskID,
		ChannelID:      task.ChannelID,
		StartedAt:      time.Now(),
		OriginalPrompt: task.Prompt,
	}

	if a.verbosity != display.Quiet {
		fmt.Print(display.FormatRequestBanner(task.Prompt, a.opts.CWD, a.opts.Model))
	}

	ctrlCtx, ctrlCancel := context.WithTimeout(ctx, 30*time.Second)
	err := a.opts.Relay.Send(ctrlCtx, &protocol.TaskStarted{
		Type:      protocol.MsgTypeTaskStarted,
		TaskID:    task.TaskID,
		SessionID: a.opts.SessionID,
		ChannelID: tc.ChannelID,
		StartedAt: tc.StartedAt.UTC().Format(time.RFC3339Nano),
	})
	ctrlCancel()
	if err != nil {
		return fmt.Errorf("send taskStarted: %w", err)
	}

	err := a.runExecutor(taskCtx, tc, task.Prompt)

	// If the task context was cancelled (user hit ESC or timeout), send the
	// appropriate message and loop back for the next task.
	if taskCtx.Err() != nil && ctx.Err() == nil {
		// Determine if this was a timeout or a user cancellation
		if taskCtx.Err() == context.DeadlineExceeded {
			errCtx, errCancel := context.WithTimeout(ctx, 30*time.Second)
			_ = a.opts.Relay.Send(errCtx, &protocol.TaskError{
				Type:      protocol.MsgTypeTaskError,
				TaskID:    task.TaskID,
				SessionID: a.opts.SessionID,
				ChannelID: tc.ChannelID,
				Error:     fmt.Sprintf("task timed out after %s", a.taskTimeout),
			})
			errCancel()
			return nil
		}
		cancelCtx, cancelCancel := context.WithTimeout(ctx, 30*time.Second)
		_ = a.opts.Relay.Send(cancelCtx, &protocol.TaskCancelled{
			Type:      protocol.MsgTypeTaskCancelled,
			TaskID:    task.TaskID,
			SessionID: a.opts.SessionID,
			ChannelID: tc.ChannelID,
		})
		cancelCancel()
		return nil
	}

	return err
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/session/ -run "TestActorTaskTimeout|TestActorLastActiveAt" -v
```

Expected: PASS.

Note: `TestActorTaskTimeout` requires the `fake-claude` binary to support a `FAKE_CLAUDE_SLEEP` environment variable that causes it to sleep N seconds before producing output. If this env var is not yet supported, add it to `cmd/fake-claude/main.go`:

```go
if sleepStr := os.Getenv("FAKE_CLAUDE_SLEEP"); sleepStr != "" {
	if secs, err := strconv.Atoi(sleepStr); err == nil {
		time.Sleep(time.Duration(secs) * time.Second)
	}
}
```

Add this before the result event output in `cmd/fake-claude/main.go`.

- [ ] **Step 5: Run all existing actor tests for regressions**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/session/ -v
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/session/actor.go internal/session/actor_test.go cmd/fake-claude/main.go
git commit -m "feat(actor): add per-task timeout and lastActiveAt tracking

Tasks get a context deadline from taskTimeout. Timeout fires
DeadlineExceeded which sends TaskError. lastActiveAt updates on each
task completion for reaper idle detection."
```

---

### Task 5: Add PID file tracking to Actor's executor calls

**Files:**
- Modify: `internal/session/actor.go`

- [ ] **Step 1: Write the failing test for PID file creation**

Append to `internal/session/actor_test.go`:

```go
func TestActorWritesPIDFile(t *testing.T) {
	binPath := buildFakeClaude(t)
	relay := newFakeRelay()
	pidDir := t.TempDir()

	actor, err := NewActor(Options{
		SessionID:  "sess-pid",
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Relay:      relay,
	})
	if err != nil {
		t.Fatal(err)
	}
	actor.pidDir = pidDir
	defer actor.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = actor.Run(ctx) }()

	if err := actor.SendTask(protocol.Task{
		TaskID:    "t1",
		SessionID: "sess-pid",
		ChannelID: "ch1",
		Prompt:    "hello",
	}); err != nil {
		t.Fatal(err)
	}

	if !relay.waitForTaskComplete(t, 10*time.Second) {
		t.Fatal("timed out waiting for TaskComplete")
	}

	// After completion, PID file should be cleaned up
	entries, err := os.ReadDir(pidDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected PID file to be cleaned up, found %d files", len(entries))
	}
}
```

Add `"os"` to the test imports if not present.

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/session/ -run TestActorWritesPIDFile -v
```

Expected: FAIL — `pidDir` field not defined.

- [ ] **Step 3: Wire PID file tracking into the actor**

Add a `pidDir` field to the `Actor` struct:

```go
	// pidDir is the directory for PID files. Empty disables PID tracking.
	pidDir string
```

Update `runExecutor` to set PID callbacks:

```go
func (a *Actor) runExecutor(ctx context.Context, tc *taskContext, prompt string) error {
	exec := claude.NewExecutor(claude.Options{
		BinaryPath:     a.opts.BinaryPath,
		CWD:            a.opts.CWD,
		Model:          a.opts.Model,
		Effort:         a.opts.Effort,
		PermissionMode: a.opts.PermissionMode,
		SystemPrompt:   a.opts.SystemPrompt,
		ResumeSession:  a.claudeSessionID,
		AllowedTools:   a.allowedTools,
		Prompt:         prompt,
	})

	if a.pidDir != "" {
		exec.OnPIDStart = func(pid int) {
			path := filepath.Join(a.pidDir, fmt.Sprintf("%s.pid", tc.TaskID))
			if err := pidfile.Write(path, pid); err != nil {
				log.Printf("[actor] write pid file: %v", err)
			}
		}
		exec.OnPIDExit = func(pid int) {
			path := filepath.Join(a.pidDir, fmt.Sprintf("%s.pid", tc.TaskID))
			pidfile.Remove(path)
		}
	}

	var resultRaw json.RawMessage

	err := exec.Run(ctx, func(e claude.Event) error {
		a.seq++
		next := a.seq

		// Display to terminal
		if a.stream.Handle(e.Raw) {
			// stream_event handled
		} else if a.stream.ShouldSkipText() {
			if s := display.FormatEventSkipText(e.Raw, a.verbosity); s != "" {
				fmt.Println(s)
			}
			if e.Type != "assistant" {
				a.stream.ConsumeSkip()
			}
		} else {
			if s := display.FormatEvent(e.Raw, a.verbosity); s != "" {
				fmt.Println(s)
			}
		}

		// Send to relay
		frame := &protocol.Stream{
			Type:           protocol.MsgTypeStream,
			SessionID:      a.opts.SessionID,
			ChannelID:      tc.ChannelID,
			SequenceNumber: next,
			Event:          e.Raw,
		}
		sendCtx, sendCancel := context.WithTimeout(ctx, 5*time.Second)
		if err := a.opts.Relay.Send(sendCtx, frame); err != nil {
			log.Printf("[actor] relay send failed: session=%s seq=%d err=%v",
				a.opts.SessionID, next, err)
		}
		sendCancel()

		if e.Type == "result" {
			resultRaw = make([]byte, len(e.Raw))
			copy(resultRaw, e.Raw)
		}
		return nil
	})

	if err != nil {
		return err
	}

	if resultRaw == nil {
		return fmt.Errorf("executor exited without result event")
	}

	return a.handleResult(ctx, tc, resultRaw)
}
```

Add these imports to `internal/session/actor.go`:

```go
	"path/filepath"

	"github.com/gsd-build/daemon/internal/pidfile"
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/session/ -run TestActorWritesPIDFile -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/session/actor.go internal/session/actor_test.go
git commit -m "feat(actor): write PID files for child processes

Each Claude subprocess gets a PID file in pidDir named {taskID}.pid.
Cleaned up on process exit via OnPIDExit callback."
```

---

### Task 6: Add concurrency control and memory check to Manager

**Files:**
- Modify: `internal/session/manager.go`
- Create: `internal/session/manager_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/session/manager_test.go
package session

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gsd-build/daemon/internal/config"
	"github.com/gsd-build/daemon/internal/display"
)

func TestManagerRejectAtCapacity(t *testing.T) {
	relay := newFakeRelay()
	cfg := &config.Config{MaxConcurrentTasks: 1}

	mgr := NewManager(ManagerOptions{
		BinaryPath: "fake",
		Relay:      relay,
		Verbosity:  display.Quiet,
		Config:     cfg,
	})

	ctx := context.Background()

	// Spawn first actor
	a1, err := mgr.Spawn(ctx, Options{
		SessionID: "s1",
		CWD:       t.TempDir(),
	})
	if err != nil {
		t.Fatalf("spawn s1: %v", err)
	}

	// Simulate in-flight task on a1
	a1.taskMu.Lock()
	a1.taskID = "t1"
	a1.taskMu.Unlock()

	// Second spawn should fail because we're at capacity
	_, err = mgr.Spawn(ctx, Options{
		SessionID: "s2",
		CWD:       t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected capacity error")
	}
	if !strings.Contains(err.Error(), "at capacity") {
		t.Errorf("expected 'at capacity' error, got: %v", err)
	}
}

func TestManagerReturnsExistingActor(t *testing.T) {
	relay := newFakeRelay()
	cfg := &config.Config{MaxConcurrentTasks: 2}

	mgr := NewManager(ManagerOptions{
		BinaryPath: "fake",
		Relay:      relay,
		Verbosity:  display.Quiet,
		Config:     cfg,
	})

	ctx := context.Background()

	a1, err := mgr.Spawn(ctx, Options{SessionID: "s1", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	a2, err := mgr.Spawn(ctx, Options{SessionID: "s1", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	if a1 != a2 {
		t.Error("expected same actor for same session")
	}
}

func TestManagerDefaultConcurrency(t *testing.T) {
	cfg := &config.Config{} // MaxConcurrentTasks = 0, should use NumCPU
	expected := runtime.NumCPU()
	if cfg.EffectiveMaxConcurrentTasks() != expected {
		t.Errorf("expected %d, got %d", expected, cfg.EffectiveMaxConcurrentTasks())
	}
}

func TestManagerInFlightCount(t *testing.T) {
	relay := newFakeRelay()
	cfg := &config.Config{MaxConcurrentTasks: 10}

	mgr := NewManager(ManagerOptions{
		BinaryPath: "fake",
		Relay:      relay,
		Verbosity:  display.Quiet,
		Config:     cfg,
	})

	ctx := context.Background()

	a1, _ := mgr.Spawn(ctx, Options{SessionID: "s1", CWD: t.TempDir()})
	a2, _ := mgr.Spawn(ctx, Options{SessionID: "s2", CWD: t.TempDir()})

	if mgr.InFlightCount() != 0 {
		t.Errorf("expected 0, got %d", mgr.InFlightCount())
	}

	a1.taskMu.Lock()
	a1.taskID = "t1"
	a1.taskMu.Unlock()

	if mgr.InFlightCount() != 1 {
		t.Errorf("expected 1, got %d", mgr.InFlightCount())
	}

	a2.taskMu.Lock()
	a2.taskID = "t2"
	a2.taskMu.Unlock()

	if mgr.InFlightCount() != 2 {
		t.Errorf("expected 2, got %d", mgr.InFlightCount())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/session/ -run "TestManagerReject|TestManagerReturns|TestManagerDefault|TestManagerInFlight" -v
```

Expected: FAIL — `ManagerOptions`, `InFlightCount` not defined.

- [ ] **Step 3: Implement concurrency control in Manager**

Replace `internal/session/manager.go`:

```go
package session

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"sync"

	"github.com/gsd-build/daemon/internal/config"
	"github.com/gsd-build/daemon/internal/display"
)

// ManagerOptions configures a new Manager.
type ManagerOptions struct {
	BinaryPath string
	Relay      RelaySender
	Verbosity  display.VerbosityLevel
	Config     *config.Config
	PIDDir     string // directory for child PID files; empty disables
}

// Manager holds a pool of session actors, keyed by sessionID.
type Manager struct {
	mu     sync.Mutex
	actors map[string]*Actor

	relay      RelaySender
	binaryPath string
	verbosity  display.VerbosityLevel
	cfg        *config.Config
	pidDir     string
}

// NewManager constructs a Manager.
func NewManager(opts ManagerOptions) *Manager {
	return &Manager{
		actors:     make(map[string]*Actor),
		relay:      opts.Relay,
		binaryPath: opts.BinaryPath,
		verbosity:  opts.Verbosity,
		cfg:        opts.Config,
		pidDir:     opts.PIDDir,
	}
}

// InFlightCount returns the number of actors with in-flight tasks.
func (m *Manager) InFlightCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, a := range m.actors {
		if a.HasInFlightTask() {
			count++
		}
	}
	return count
}

// ActiveCount returns the total number of actors and how many are executing.
func (m *Manager) ActiveCount() (total int, executing int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	total = len(m.actors)
	for _, a := range m.actors {
		if a.InFlightTaskID() != "" {
			executing++
		}
	}
	return total, executing
}

// Spawn creates and starts a new actor for the session.
// Returns an existing actor if one already exists for the session.
// Returns an error if the machine is at capacity.
func (m *Manager) Spawn(
	ctx context.Context,
	opts Options,
) (*Actor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing := m.actors[opts.SessionID]; existing != nil {
		return existing, nil
	}

	// Concurrency check
	maxTasks := m.cfg.EffectiveMaxConcurrentTasks()
	inFlight := 0
	for _, a := range m.actors {
		if a.HasInFlightTask() {
			inFlight++
		}
	}
	if inFlight >= maxTasks {
		return nil, fmt.Errorf("machine at capacity — %d/%d tasks running, try again shortly", inFlight, maxTasks)
	}

	// Memory safety net: reject if available memory < 10% of total
	if memoryTooLow() {
		return nil, fmt.Errorf("machine at capacity — available memory below 10%%, try again shortly")
	}

	if opts.Relay == nil {
		opts.Relay = m.relay
	}
	if opts.BinaryPath == "" {
		opts.BinaryPath = m.binaryPath
	}
	opts.Verbosity = m.verbosity

	actor, err := NewActor(opts)
	if err != nil {
		return nil, fmt.Errorf("new actor: %w", err)
	}
	actor.taskTimeout = m.cfg.EffectiveTaskTimeout()
	actor.pidDir = m.pidDir
	m.actors[opts.SessionID] = actor

	sessionID := opts.SessionID
	go func() {
		err := actor.Run(ctx)
		if err == nil || ctx.Err() != nil {
			return
		}
		log.Printf("[session] actor.Run exited with error: session=%s err=%v", sessionID, err)
		if m.verbosity != display.Quiet {
			fmt.Print(display.FormatErrorBanner(err.Error()))
		}
	}()
	return actor, nil
}

// Remove removes an actor from the map. Called by the reaper.
func (m *Manager) Remove(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.actors[sessionID]; ok {
		_ = a.Stop()
		delete(m.actors, sessionID)
	}
}

// StopAll stops every actor. Called on daemon shutdown.
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.actors {
		_ = a.Stop()
	}
	m.actors = make(map[string]*Actor)
}

// IdleActors returns session IDs of actors idle longer than maxIdle.
func (m *Manager) IdleActors(maxIdle interface{ Duration() }) []string {
	// This is implemented by the reaper; see reapIdleActors.
	return nil
}

// memoryTooLow returns true if available system memory is below 10% of total.
// Uses platform-specific syscalls to check actual system memory, not Go heap.
func memoryTooLow() bool {
	total, avail, err := systemMemory()
	if err != nil || total == 0 {
		return false // can't determine — don't block tasks
	}
	return float64(avail) < float64(total)*0.10
}

// systemMemory returns total and available system RAM in bytes.
// Implemented per-platform in mem_darwin.go and mem_linux.go.
//
// mem_darwin.go:
//   func systemMemory() (total, avail uint64, err error) {
//       // Use syscall.Sysctl("hw.memsize") for total
//       // Use vm_stat or host_statistics64 for free+inactive pages
//   }
//
// mem_linux.go:
//   func systemMemory() (total, avail uint64, err error) {
//       // Parse /proc/meminfo for MemTotal and MemAvailable
//   }
//
// These are separate files with build tags. The executor creates them
// alongside this file in internal/session/.
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/session/ -run "TestManagerReject|TestManagerReturns|TestManagerDefault|TestManagerInFlight" -v
```

Expected: PASS.

- [ ] **Step 5: Run all existing tests to verify no regressions**

Note: Existing actor tests call `NewManager(binaryPath, relay, verbosity)` with the old signature. Those tests don't go through the manager, so they should still pass. If any existing code in other packages calls `NewManager`, update those call sites to use `ManagerOptions`. Check:

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
grep -r "NewManager(" --include="*.go" | grep -v "_test.go" | grep -v "manager.go"
```

Update any callers to pass `ManagerOptions`. The main call site is likely in the daemon loop (`internal/loop/` or `main.go`). Update it:

```go
// Old:
// mgr := session.NewManager(binaryPath, relay, verbosity)

// New:
mgr := session.NewManager(session.ManagerOptions{
    BinaryPath: binaryPath,
    Relay:      relay,
    Verbosity:  verbosity,
    Config:     cfg,
    PIDDir:     pidDir,
})
```

Then run:

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/session/ -v
go build ./...
```

Expected: all PASS, build succeeds.

- [ ] **Step 6: Commit**

```bash
git add internal/session/manager.go internal/session/manager_test.go
git commit -m "feat(manager): add concurrency control and memory safety net

Spawn rejects new tasks when in-flight count hits the configured max
(default: runtime.NumCPU). Also rejects when Go heap usage exceeds
90% of Sys memory. Sets taskTimeout and pidDir on new actors."
```

---

### Task 7: Add actor reaper goroutine

**Files:**
- Modify: `internal/session/manager.go`
- Modify: `internal/session/manager_test.go`

- [ ] **Step 1: Write the failing test for the reaper**

Append to `internal/session/manager_test.go`:

```go
func TestReaperRemovesIdleActors(t *testing.T) {
	relay := newFakeRelay()
	cfg := &config.Config{MaxConcurrentTasks: 10}

	mgr := NewManager(ManagerOptions{
		BinaryPath: "fake",
		Relay:      relay,
		Verbosity:  display.Quiet,
		Config:     cfg,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a1, _ := mgr.Spawn(ctx, Options{SessionID: "s1", CWD: t.TempDir()})
	_, _ = mgr.Spawn(ctx, Options{SessionID: "s2", CWD: t.TempDir()})

	// Make s1 idle for "long ago"
	a1.taskMu.Lock()
	a1.lastActiveAt = time.Now().Add(-1 * time.Hour)
	a1.taskMu.Unlock()

	// Run one reap cycle with a 30-minute idle threshold
	reaped := mgr.ReapIdleActors(30 * time.Minute)

	if reaped != 1 {
		t.Errorf("expected 1 reaped, got %d", reaped)
	}

	if mgr.Get("s1") != nil {
		t.Error("expected s1 to be removed")
	}
	if mgr.Get("s2") == nil {
		t.Error("expected s2 to remain")
	}
}

func TestReaperSkipsActorsWithInFlightTasks(t *testing.T) {
	relay := newFakeRelay()
	cfg := &config.Config{MaxConcurrentTasks: 10}

	mgr := NewManager(ManagerOptions{
		BinaryPath: "fake",
		Relay:      relay,
		Verbosity:  display.Quiet,
		Config:     cfg,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a1, _ := mgr.Spawn(ctx, Options{SessionID: "s1", CWD: t.TempDir()})

	// Make s1 idle for "long ago" but with an in-flight task
	a1.taskMu.Lock()
	a1.lastActiveAt = time.Now().Add(-1 * time.Hour)
	a1.taskID = "t1" // in-flight
	a1.taskMu.Unlock()

	reaped := mgr.ReapIdleActors(30 * time.Minute)

	if reaped != 0 {
		t.Errorf("expected 0 reaped (in-flight task), got %d", reaped)
	}

	if mgr.Get("s1") == nil {
		t.Error("expected s1 to remain (has in-flight task)")
	}
}

func TestStartReaper(t *testing.T) {
	relay := newFakeRelay()
	cfg := &config.Config{MaxConcurrentTasks: 10}

	mgr := NewManager(ManagerOptions{
		BinaryPath: "fake",
		Relay:      relay,
		Verbosity:  display.Quiet,
		Config:     cfg,
	})

	ctx, cancel := context.WithCancel(context.Background())

	a1, _ := mgr.Spawn(ctx, Options{SessionID: "s1", CWD: t.TempDir()})
	a1.taskMu.Lock()
	a1.lastActiveAt = time.Now().Add(-1 * time.Hour)
	a1.taskMu.Unlock()

	// Start reaper with short tick for testing
	mgr.StartReaper(ctx, 50*time.Millisecond, 30*time.Minute)

	// Wait for one tick
	time.Sleep(200 * time.Millisecond)
	cancel()

	if mgr.Get("s1") != nil {
		t.Error("expected reaper to remove idle actor s1")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/session/ -run "TestReaper|TestStartReaper" -v
```

Expected: FAIL — `ReapIdleActors`, `StartReaper` not defined.

- [ ] **Step 3: Implement reaper**

Add to `internal/session/manager.go`:

```go
// ReapIdleActors stops and removes actors idle longer than maxIdle.
// Actors with in-flight tasks are never reaped. Returns the count of reaped actors.
func (m *Manager) ReapIdleActors(maxIdle time.Duration) int {
	m.mu.Lock()
	var toReap []string
	cutoff := time.Now().Add(-maxIdle)
	for id, a := range m.actors {
		if a.HasInFlightTask() {
			continue
		}
		if a.LastActiveAt().Before(cutoff) {
			toReap = append(toReap, id)
		}
	}
	m.mu.Unlock()

	for _, id := range toReap {
		m.Remove(id)
		log.Printf("[reaper] reaped idle actor: session=%s", id)
	}
	return len(toReap)
}

// StartReaper launches a goroutine that reaps idle actors on a tick interval.
// Runs until ctx is cancelled.
func (m *Manager) StartReaper(ctx context.Context, tick time.Duration, maxIdle time.Duration) {
	go func() {
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if n := m.ReapIdleActors(maxIdle); n > 0 {
					log.Printf("[reaper] reaped %d idle actor(s)", n)
				}
			}
		}
	}()
}
```

Add `"time"` to the import block.

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/session/ -run "TestReaper|TestStartReaper" -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/session/manager.go internal/session/manager_test.go
git commit -m "feat(manager): add actor reaper goroutine

5-minute tick (configurable for tests). Reaps actors idle >30 minutes.
Actors with in-flight tasks are never reaped. Prevents goroutine leaks
from abandoned sessions."
```

---

### Task 8: Wire stale PID cleanup into daemon startup

**Files:**
- Modify: daemon entry point (the file that calls `session.NewManager`)

- [ ] **Step 1: Locate the daemon entry point**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
grep -r "NewManager" --include="*.go" -l
```

- [ ] **Step 2: Add PID cleanup call at startup**

At the daemon startup point (where the Manager is created), add:

```go
import "github.com/gsd-build/daemon/internal/pidfile"
```

Before creating the Manager:

```go
// Clean up stale PID files from previous crashes
pidDir, err := pidfile.Dir()
if err != nil {
	log.Printf("[daemon] pid dir: %v", err)
} else {
	if n := pidfile.CleanStale(pidDir); n > 0 {
		log.Printf("[daemon] cleaned %d stale PID file(s)", n)
	}
}
```

Pass `pidDir` into `ManagerOptions`:

```go
mgr := session.NewManager(session.ManagerOptions{
    BinaryPath: binaryPath,
    Relay:      relay,
    Verbosity:  verbosity,
    Config:     cfg,
    PIDDir:     pidDir,
})
```

Start the reaper after the manager is created:

```go
mgr.StartReaper(ctx, 5*time.Minute, 30*time.Minute)
```

- [ ] **Step 3: Verify build**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go build ./...
```

Expected: build succeeds.

- [ ] **Step 4: Run full test suite**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./...
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(daemon): wire PID cleanup and reaper into startup

Cleans stale PID files on boot. Starts the actor reaper goroutine
with 5-minute tick and 30-minute idle threshold. Passes config-based
concurrency and timeout settings through to the Manager."
```

---

### Task 9: Integration test — full concurrency + timeout + reaper flow

**Files:**
- Modify: `internal/session/manager_test.go`

- [ ] **Step 1: Write integration test**

Append to `internal/session/manager_test.go`:

```go
func TestIntegrationConcurrencyAndTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	binPath := buildFakeClaude(t)
	relay := newFakeRelay()
	pidDir := t.TempDir()
	cfg := &config.Config{MaxConcurrentTasks: 2}

	mgr := NewManager(ManagerOptions{
		BinaryPath: binPath,
		Relay:      relay,
		Verbosity:  display.Quiet,
		Config:     cfg,
		PIDDir:     pidDir,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start reaper with short tick
	mgr.StartReaper(ctx, 100*time.Millisecond, 2*time.Second)

	// Spawn two sessions — both should succeed
	a1, err := mgr.Spawn(ctx, Options{SessionID: "s1", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("spawn s1: %v", err)
	}

	a2, err := mgr.Spawn(ctx, Options{SessionID: "s2", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("spawn s2: %v", err)
	}

	// Send tasks to both
	_ = a1.SendTask(protocol.Task{TaskID: "t1", SessionID: "s1", ChannelID: "ch1", Prompt: "hello"})
	_ = a2.SendTask(protocol.Task{TaskID: "t2", SessionID: "s2", ChannelID: "ch2", Prompt: "world"})

	// Wait for both to complete
	gotBoth := relay.waitFor(t, 15*time.Second, func(frames []any) bool {
		count := 0
		for _, f := range frames {
			if _, ok := f.(*protocol.TaskComplete); ok {
				count++
			}
		}
		return count >= 2
	})
	if !gotBoth {
		t.Fatal("timed out waiting for both tasks to complete")
	}

	// Both should now be idle. Wait for reaper to clean them up (2s idle threshold).
	time.Sleep(3 * time.Second)

	total, _ := mgr.ActiveCount()
	if total != 0 {
		t.Errorf("expected reaper to clean up idle actors, got %d active", total)
	}

	// Verify PID files are cleaned up
	entries, _ := os.ReadDir(pidDir)
	if len(entries) != 0 {
		t.Errorf("expected PID files to be cleaned up, found %d", len(entries))
	}
}
```

Add `"os"` and `protocol "github.com/gsd-build/protocol-go"` to the test imports if not present.

- [ ] **Step 2: Run integration test**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/session/ -run TestIntegrationConcurrencyAndTimeout -v -count=1
```

Expected: PASS.

- [ ] **Step 3: Run full test suite one final time**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./... -v
```

Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/session/manager_test.go
git commit -m "test(session): add integration test for concurrency + timeout + reaper

End-to-end test spawning two sessions, running tasks, verifying
completion, then waiting for the reaper to clean up idle actors
and PID files."
```
