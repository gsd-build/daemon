# Spawn-Per-Task Executor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the long-lived `claude -p --input-format stream-json` executor with a spawn-per-task model using `claude -p --resume <id> "prompt"`, eliminating idle process memory, PTY hacks, stdin pipes, and RestartWithGrant complexity. Includes 6 bundled reliability fixes.

**Architecture:** The executor becomes a single-shot `Run()` that spawns claude, reads stdout until EOF, and returns. The actor holds a task channel and spawns fresh executors on demand. Permission grants are handled by spawning with `--allowedTools` — no kill-and-restart dance. Session continuity via `--resume`.

**Tech Stack:** Go 1.25, standard library. No new dependencies. Removes `creack/pty` and `golang.org/x/term` dependencies.

**Spec:** `docs/superpowers/specs/2026-04-11-spawn-per-task-executor-design.md`

---

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `internal/claude/executor.go` | **Rewrite** | Single-shot `Run()` — build args, spawn process, pipe stdout, parse events, capture stderr, return |
| `internal/claude/pty_unix.go` | **Delete** | No longer needed — stdout is a regular pipe |
| `internal/claude/executor_test.go` | **Rewrite** | Adapt all tests from Start/Send/Close to Run() pattern |
| `cmd/fake-claude/main.go` | **Rewrite** | Read prompt from CLI args instead of stdin; emit events and exit |
| `cmd/fake-claude-blockbuf/main.go` | **Delete** | Only existed to test PTY buffering, which is removed |
| `internal/session/actor.go` | **Rewrite** | Task channel, spawn-per-task loop, simplified permission handling |
| `internal/session/actor_test.go` | **Update** | Adapt to channel-based task delivery |
| `internal/session/manager.go` | **Update** | Remove stale comments referencing PTY/ready channel |
| `internal/wal/wal.go` | **Fix** | Prune temp cleanup + ReadFrom-Prune race fix |
| `internal/wal/recover.go` | **Fix** | Log corrupt WAL files during scan |
| `internal/relay/client.go` | **Fix** | Add Welcome read timeout |
| `tests/e2e/daemon_e2e_test.go` | **Update** | Adapt to new fake-claude (no stdin reading) |
| `go.mod` / `go.sum` | **Update** | Remove `creack/pty` and `golang.org/x/term` if no longer used |

---

### Task 1: Rewrite executor.go as single-shot Run()

**Files:**
- Rewrite: `internal/claude/executor.go`
- Delete: `internal/claude/pty_unix.go`

This is the foundation — everything else depends on it. The executor becomes dramatically simpler: build args, spawn, parse stdout, wait, return.

- [ ] **Step 1: Delete pty_unix.go**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
rm internal/claude/pty_unix.go
```

- [ ] **Step 2: Rewrite executor.go**

Replace the entire file with:

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
		e.opts.BinaryPath, e.opts.CWD, e.opts.Model, truncate(e.opts.Prompt, 80))

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
	for _, tool := range e.opts.AllowedTools {
		args = append(args, "--allowedTools", tool)
	}
	// Prompt as positional argument — must be last
	args = append(args, e.opts.Prompt)

	cmd := exec.CommandContext(ctx, e.opts.BinaryPath, args...)
	cmd.Dir = e.opts.CWD
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
		return fmt.Errorf("start: %w", err)
	}

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

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
```

Note: `stderr_buffer.go` and `parser.go` are unchanged — they still work as-is.

- [ ] **Step 3: Verify the package compiles (tests won't pass yet — test file references old API)**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go build ./internal/claude/`
Expected: Compilation errors from test file referencing `Start`, `Send`, `Close`, `NewExecutor` old signatures. This is expected — tests are updated in Task 3.

- [ ] **Step 4: Commit**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
git add internal/claude/executor.go
git rm internal/claude/pty_unix.go
git commit -m "feat(executor): rewrite as single-shot Run(), remove PTY"
```

---

### Task 2: Rewrite fake-claude for CLI-arg mode

**Files:**
- Rewrite: `cmd/fake-claude/main.go`
- Delete: `cmd/fake-claude-blockbuf/main.go`

fake-claude must now read the prompt from os.Args (the last positional arg) instead of reading NDJSON from stdin. It emits events and exits immediately.

- [ ] **Step 1: Delete fake-claude-blockbuf**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
rm -rf cmd/fake-claude-blockbuf
```

Also delete `cmd/repro-stdout` if it exists and is only for PTY debugging:

```bash
ls cmd/repro-stdout/ 2>/dev/null && rm -rf cmd/repro-stdout || echo "not found, skip"
```

- [ ] **Step 2: Rewrite fake-claude/main.go**

Replace the entire file with:

```go
// fake-claude is a test helper that mimics `claude -p --output-format stream-json`.
// It reads the prompt from the last positional CLI argument, emits scripted
// stream-json events to stdout, and exits.
//
// Env vars:
//
//	FAKE_CLAUDE_DENY_TOOL=<name>  — emit permission_denials in the result
//	                                (unless the tool is in --allowedTools)
//	FAKE_CLAUDE_SESSION_ID=<id>   — override the synthetic session id
//	FAKE_CLAUDE_ARGS_FILE=<path>  — write os.Args[1:] as JSON to this file
//	FAKE_CLAUDE_STDERR=<text>     — write this string to stderr at startup
//	FAKE_CLAUDE_EXIT_CODE=<n>     — exit with this code immediately after stderr
//
// Usage: invoked by the daemon as `claude -p ... "prompt"` during tests.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func main() {
	// Write argv to file if requested
	if argsFile := os.Getenv("FAKE_CLAUDE_ARGS_FILE"); argsFile != "" {
		data, _ := json.Marshal(os.Args[1:])
		_ = os.WriteFile(argsFile, data, 0o600)
	}

	// Stderr and exit code hooks
	if msg := os.Getenv("FAKE_CLAUDE_STDERR"); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}
	if codeStr := os.Getenv("FAKE_CLAUDE_EXIT_CODE"); codeStr != "" {
		if code, err := strconv.Atoi(codeStr); err == nil {
			os.Exit(code)
		}
	}

	denyTool := os.Getenv("FAKE_CLAUDE_DENY_TOOL")
	sessionID := os.Getenv("FAKE_CLAUDE_SESSION_ID")
	if sessionID == "" {
		sessionID = "fake-session-123"
	}

	// Detect --allowedTools
	allowedSet := make(map[string]bool)
	for i, arg := range os.Args {
		if arg == "--allowedTools" && i+1 < len(os.Args) {
			for _, t := range strings.Split(os.Args[i+1], ",") {
				allowedSet[strings.TrimSpace(t)] = true
			}
		}
	}

	// Emit events to stdout

	// 1. stream_event partial
	streamEvent := map[string]any{
		"type": "stream_event",
		"event": map[string]any{
			"delta": map[string]any{"text": "fake delta"},
		},
	}
	_ = json.NewEncoder(os.Stdout).Encode(streamEvent)
	os.Stdout.Sync()

	// 2. assistant text block
	assistant := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": "fake response"},
			},
		},
	}
	_ = json.NewEncoder(os.Stdout).Encode(assistant)
	os.Stdout.Sync()

	// 3. result
	result := map[string]any{
		"type":           "result",
		"subtype":        "success",
		"total_cost_usd": 0.0001,
		"duration_ms":    42,
		"usage": map[string]int{
			"input_tokens":  10,
			"output_tokens": 5,
		},
		"session_id": sessionID,
	}

	// Emit permission denial if configured and tool not already allowed
	if denyTool != "" && !allowedSet[denyTool] {
		result["permission_denials"] = []map[string]any{
			{
				"tool_name":   denyTool,
				"tool_use_id": "toolu_fake_001",
				"tool_input": map[string]any{
					"file_path": "/tmp/fake.txt",
					"content":   "fake content",
				},
			},
		}
	}

	_ = json.NewEncoder(os.Stdout).Encode(result)
	os.Stdout.Sync()

	// Exit cleanly — process lifecycle is done
}
```

- [ ] **Step 3: Verify fake-claude compiles**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go build ./cmd/fake-claude/`
Expected: Clean build.

- [ ] **Step 4: Commit**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
git add cmd/fake-claude/main.go
git rm -rf cmd/fake-claude-blockbuf
git rm -rf cmd/repro-stdout 2>/dev/null || true
git commit -m "feat(fake-claude): rewrite for CLI-arg mode, remove blockbuf variant"
```

---

### Task 3: Rewrite executor_test.go

**Files:**
- Rewrite: `internal/claude/executor_test.go`

All tests adapt from `Start/Send/Close` pattern to `Run()` with prompt in Options.

- [ ] **Step 1: Rewrite executor_test.go**

Replace the entire file with:

```go
package claude

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func buildFakeClaude(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	daemonDir := filepath.Join(filepath.Dir(thisFile), "..", "..")
	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "fake-claude")
	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/fake-claude")
	cmd.Dir = daemonDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build fake-claude: %v\n%s", err, out)
	}
	return binPath
}

func readArgsFile(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read args file: %v", err)
	}
	var argv []string
	if err := json.Unmarshal(data, &argv); err != nil {
		t.Fatalf("unmarshal args file: %v", err)
	}
	return argv
}

func TestExecutorRoundTrip(t *testing.T) {
	binPath := buildFakeClaude(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	e := NewExecutor(Options{
		BinaryPath:     binPath,
		CWD:            t.TempDir(),
		Model:          "test-model",
		Effort:         "max",
		PermissionMode: "acceptEdits",
		Prompt:         "hello",
	})

	var (
		mu     sync.Mutex
		events []Event
	)
	err := e.Run(ctx, func(ev Event) error {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(events))
	}

	last := events[len(events)-1]
	if last.Type != "result" {
		t.Errorf("expected last event type=result, got %s", last.Type)
	}

	var payload map[string]any
	_ = json.Unmarshal(last.Raw, &payload)
	if payload["session_id"] != "fake-session-123" {
		t.Errorf("expected session_id=fake-session-123, got %v", payload["session_id"])
	}
}

func TestExecutorResumeFlag(t *testing.T) {
	binPath := buildFakeClaude(t)
	argsFile := filepath.Join(t.TempDir(), "argv.json")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const resumeID = "test-claude-session-abc"
	e := NewExecutor(Options{
		BinaryPath:    binPath,
		CWD:           t.TempDir(),
		ResumeSession: resumeID,
		Prompt:        "test prompt",
		Env:           []string{"FAKE_CLAUDE_ARGS_FILE=" + argsFile},
	})

	_ = e.Run(ctx, func(_ Event) error { return nil })

	argv := readArgsFile(t, argsFile)
	found := false
	for i, arg := range argv {
		if arg == "--resume" && i+1 < len(argv) && argv[i+1] == resumeID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected --resume %s in argv, got: %v", resumeID, argv)
	}
}

func TestExecutorNoResumeFlag(t *testing.T) {
	binPath := buildFakeClaude(t)
	argsFile := filepath.Join(t.TempDir(), "argv.json")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	e := NewExecutor(Options{
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Prompt:     "test prompt",
		Env:        []string{"FAKE_CLAUDE_ARGS_FILE=" + argsFile},
	})

	_ = e.Run(ctx, func(_ Event) error { return nil })

	argv := readArgsFile(t, argsFile)
	for _, arg := range argv {
		if arg == "--resume" {
			t.Errorf("expected no --resume in argv, got: %v", argv)
			break
		}
	}
}

func TestExecutorPromptInArgs(t *testing.T) {
	binPath := buildFakeClaude(t)
	argsFile := filepath.Join(t.TempDir(), "argv.json")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const prompt = "Fix the login bug"
	e := NewExecutor(Options{
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Prompt:     prompt,
		Env:        []string{"FAKE_CLAUDE_ARGS_FILE=" + argsFile},
	})

	_ = e.Run(ctx, func(_ Event) error { return nil })

	argv := readArgsFile(t, argsFile)
	// Prompt should be the last argument
	if len(argv) == 0 {
		t.Fatal("empty argv")
	}
	last := argv[len(argv)-1]
	if last != prompt {
		t.Errorf("expected last arg to be prompt %q, got %q", prompt, last)
	}
}

func TestExecutorAllowedTools(t *testing.T) {
	binPath := buildFakeClaude(t)
	argsFile := filepath.Join(t.TempDir(), "argv.json")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	e := NewExecutor(Options{
		BinaryPath:   binPath,
		CWD:          t.TempDir(),
		Prompt:       "test",
		AllowedTools: []string{"Write", "Bash"},
		Env:          []string{"FAKE_CLAUDE_ARGS_FILE=" + argsFile},
	})

	_ = e.Run(ctx, func(_ Event) error { return nil })

	argv := readArgsFile(t, argsFile)
	foundWrite := false
	foundBash := false
	for i, arg := range argv {
		if arg == "--allowedTools" && i+1 < len(argv) {
			if argv[i+1] == "Write" {
				foundWrite = true
			}
			if argv[i+1] == "Bash" {
				foundBash = true
			}
		}
	}
	if !foundWrite || !foundBash {
		t.Errorf("expected --allowedTools Write and Bash, got: %v", argv)
	}
}

func TestExecutorStderrCapturedOnCrash(t *testing.T) {
	binPath := buildFakeClaude(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const stderrMsg = "simulated claude failure: invalid API key"
	e := NewExecutor(Options{
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Prompt:     "test",
		Env: []string{
			"FAKE_CLAUDE_STDERR=" + stderrMsg,
			"FAKE_CLAUDE_EXIT_CODE=2",
		},
	})

	err := e.Run(ctx, func(_ Event) error { return nil })
	if err == nil {
		t.Fatal("expected error from Run when subprocess exits non-zero, got nil")
	}
	if !strings.Contains(err.Error(), stderrMsg) {
		t.Errorf("expected error to contain stderr message %q, got: %v", stderrMsg, err)
	}
	if !strings.Contains(err.Error(), "code 2") {
		t.Errorf("expected error to mention exit code 2, got: %v", err)
	}
}

func TestExecutorContextCancellation(t *testing.T) {
	binPath := buildFakeClaude(t)

	// Use a very short timeout to force cancellation
	ctx, cancel := context.WithCancel(context.Background())

	e := NewExecutor(Options{
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Prompt:     "test",
	})

	// Cancel immediately
	cancel()

	err := e.Run(ctx, func(_ Event) error { return nil })
	// Should return nil (context cancellation is expected shutdown)
	if err != nil {
		t.Errorf("expected nil error on context cancellation, got: %v", err)
	}
}
```

- [ ] **Step 2: Run executor tests**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go test ./internal/claude/ -v -count=1`
Expected: All PASS.

- [ ] **Step 3: Commit**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
git add internal/claude/executor_test.go
git commit -m "test(executor): rewrite tests for single-shot Run() API"
```

---

### Task 4: Rewrite actor.go for spawn-per-task

**Files:**
- Rewrite: `internal/session/actor.go`

The actor becomes dramatically simpler. No long-lived executor, no mutex protecting executor state, no RestartWithGrant, no runDone channel. Instead: a task channel, a spawn-per-task loop, and clean permission handling.

- [ ] **Step 1: Rewrite actor.go**

Replace the entire file with:

```go
// Package session ties the Claude executor, WAL, and relay together
// into one "session actor" per user session.
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/gsd-build/daemon/internal/claude"
	"github.com/gsd-build/daemon/internal/display"
	"github.com/gsd-build/daemon/internal/wal"
	protocol "github.com/gsd-build/protocol-go"
)

// RelaySender is the minimal interface the actor needs to push events to the relay.
type RelaySender interface {
	Send(msg any) error
}

// Options configures a new Actor.
type Options struct {
	SessionID      string
	ChannelID      string
	BinaryPath     string
	CWD            string
	WALPath        string
	Relay          RelaySender
	Model          string
	Effort         string
	PermissionMode string
	SystemPrompt   string
	ResumeSession  string
	StartSeq       int64
	Verbosity      display.VerbosityLevel
}

// Actor drives a single Claude session using spawn-per-task execution.
// Each incoming task spawns a fresh claude process; no processes remain
// alive between tasks.
type Actor struct {
	opts Options
	log  *wal.Log

	seq       int64 // monotonic sequence counter, only touched by Run goroutine
	verbosity display.VerbosityLevel
	stream    *display.StreamHandler

	claudeSessionID string   // set from result events, used for --resume
	allowedTools    []string // accumulates as user grants permissions

	taskCh chan protocol.Task // SendTask writes here, Run reads
	permCh chan permResponse  // HandlePermissionResponse/HandleQuestionResponse write here

	stopCh chan struct{}
}

type taskContext struct {
	TaskID         string
	StartedAt      time.Time
	OriginalPrompt string
}

// pendingDenial tracks a task waiting on permission/question responses.
type pendingDenial struct {
	Denials []string
	TaskID  string
	Prompt  string
}

type permResponse struct {
	Approved bool
	ToolName string // for permission grants
	Answer   string // for question answers
}

// NewActor creates a new Actor with a WAL rooted at opts.WALPath.
func NewActor(opts Options) (*Actor, error) {
	walLog, err := wal.Open(opts.WALPath)
	if err != nil {
		return nil, fmt.Errorf("wal open: %w", err)
	}

	return &Actor{
		opts:            opts,
		log:             walLog,
		seq:             opts.StartSeq,
		verbosity:       opts.Verbosity,
		stream:          display.NewStreamHandler(os.Stdout, opts.Verbosity),
		claudeSessionID: opts.ResumeSession,
		taskCh:          make(chan protocol.Task, 1),
		permCh:          make(chan permResponse, 1),
		stopCh:          make(chan struct{}),
	}, nil
}

// LastSequence returns the highest sequence number emitted so far.
func (a *Actor) LastSequence() int64 {
	return a.seq
}

// InFlightTaskID is no longer needed in the new model — the Run loop
// processes tasks synchronously so there's no race. Kept for the
// manager's error-reporting goroutine compatibility.
func (a *Actor) InFlightTaskID() string {
	// In spawn-per-task, the task is always in-flight during Run's
	// executor call and never between tasks. Return "" since the
	// manager only checks this after Run exits with an error.
	return ""
}

// AllowedTools returns the current granted tools list.
func (a *Actor) AllowedTools() []string {
	out := make([]string, len(a.allowedTools))
	copy(out, a.allowedTools)
	return out
}

// SendTask queues a task for execution. Non-blocking if the channel has capacity.
func (a *Actor) SendTask(task protocol.Task) error {
	select {
	case a.taskCh <- task:
		return nil
	default:
		return fmt.Errorf("actor busy — task channel full")
	}
}

// Run is the actor's main loop. It waits for tasks, spawns executors, and
// handles permission flows. Blocks until ctx is canceled or Stop is called.
func (a *Actor) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-a.stopCh:
			return nil
		case task := <-a.taskCh:
			if err := a.executeTask(ctx, task); err != nil {
				log.Printf("[actor] task %s failed: %v", task.TaskID, err)
				_ = a.opts.Relay.Send(&protocol.TaskError{
					Type:      protocol.MsgTypeTaskError,
					TaskID:    task.TaskID,
					SessionID: a.opts.SessionID,
					ChannelID: a.opts.ChannelID,
					Error:     err.Error(),
				})
			}
		}
	}
}

func (a *Actor) executeTask(ctx context.Context, task protocol.Task) error {
	tc := &taskContext{
		TaskID:         task.TaskID,
		StartedAt:      time.Now(),
		OriginalPrompt: task.Prompt,
	}

	if a.verbosity != display.Quiet {
		fmt.Print(display.FormatRequestBanner(task.Prompt, a.opts.CWD, a.opts.Model))
	}

	if err := a.opts.Relay.Send(&protocol.TaskStarted{
		Type:      protocol.MsgTypeTaskStarted,
		TaskID:    task.TaskID,
		SessionID: a.opts.SessionID,
		ChannelID: a.opts.ChannelID,
		StartedAt: tc.StartedAt.UTC().Format(time.RFC3339Nano),
	}); err != nil {
		return fmt.Errorf("send taskStarted: %w", err)
	}

	return a.runExecutor(ctx, tc, task.Prompt)
}

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

		// WAL + relay
		frame := &protocol.Stream{
			Type:           protocol.MsgTypeStream,
			SessionID:      a.opts.SessionID,
			ChannelID:      a.opts.ChannelID,
			SequenceNumber: next,
			Event:          e.Raw,
		}
		frameJSON, err := json.Marshal(frame)
		if err != nil {
			return fmt.Errorf("marshal frame: %w", err)
		}
		if err := a.log.Append(next, frameJSON); err != nil {
			return fmt.Errorf("wal append: %w", err)
		}
		if err := a.opts.Relay.Send(frame); err != nil {
			log.Printf("[actor] relay send failed (WAL has entry): session=%s seq=%d err=%v",
				a.opts.SessionID, next, err)
		}

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

func (a *Actor) handleResult(ctx context.Context, tc *taskContext, raw json.RawMessage) error {
	var payload struct {
		SessionID    string  `json:"session_id"`
		TotalCostUSD float64 `json:"total_cost_usd"`
		DurationMs   int     `json:"duration_ms"`
		Usage        struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			CacheReadInput     int `json:"cache_read_input_tokens"`
			CacheCreationInput int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
		PermissionDenials []struct {
			ToolName  string          `json:"tool_name"`
			ToolUseID string          `json:"tool_use_id"`
			ToolInput json.RawMessage `json:"tool_input"`
		} `json:"permission_denials"`
	}
	_ = json.Unmarshal(raw, &payload)

	if payload.SessionID != "" {
		a.claudeSessionID = payload.SessionID
	}

	// Permission denials — wait for response then re-spawn
	if len(payload.PermissionDenials) > 0 {
		return a.handleDenials(ctx, tc, payload.PermissionDenials)
	}

	// Normal completion
	cost := fmt.Sprintf("%.6f", payload.TotalCostUSD)
	return a.opts.Relay.Send(&protocol.TaskComplete{
		Type:            protocol.MsgTypeTaskComplete,
		TaskID:          tc.TaskID,
		SessionID:       a.opts.SessionID,
		ChannelID:       a.opts.ChannelID,
		ClaudeSessionID: payload.SessionID,
		InputTokens: int64(
			payload.Usage.InputTokens +
				payload.Usage.CacheReadInput +
				payload.Usage.CacheCreationInput,
		),
		OutputTokens: int64(payload.Usage.OutputTokens),
		CostUSD:      cost,
		DurationMs:   payload.DurationMs,
	})
}

func (a *Actor) handleDenials(ctx context.Context, tc *taskContext, denials []struct {
	ToolName  string          `json:"tool_name"`
	ToolUseID string          `json:"tool_use_id"`
	ToolInput json.RawMessage `json:"tool_input"`
}) error {
	for _, denial := range denials {
		if denial.ToolName == "AskUserQuestion" {
			return a.handleQuestionDenial(ctx, tc, denial.ToolUseID, denial.ToolInput)
		}

		// Permission request
		if err := a.opts.Relay.Send(&protocol.PermissionRequest{
			Type:      protocol.MsgTypePermissionRequest,
			SessionID: a.opts.SessionID,
			ChannelID: a.opts.ChannelID,
			RequestID: denial.ToolUseID,
			ToolName:  denial.ToolName,
			ToolInput: denial.ToolInput,
		}); err != nil {
			return err
		}
		if a.verbosity != display.Quiet {
			fmt.Printf("%s⚠ PERMISSION REQUEST: %s%s\n", display.Yellow, denial.ToolName, display.Reset)
			if summary, ok := toolInputSummary(denial.ToolInput); ok {
				fmt.Printf("%s%s%s\n", display.Dim, summary, display.Reset)
			}
			fmt.Printf("%swaiting for approval...%s\n", display.Dim, display.Reset)
		}

		// Wait for response
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-a.stopCh:
			return fmt.Errorf("actor stopped while waiting for permission")
		case resp := <-a.permCh:
			if resp.Approved {
				if a.verbosity != display.Quiet {
					fmt.Printf("\n%sApproved — resuming.%s\n\n", display.Green, display.Reset)
				}
				// Add tool to allowed list and re-spawn
				a.addAllowedTool(denial.ToolName)
				return a.runExecutor(ctx, tc, tc.OriginalPrompt)
			}
			// Denied — send deny message via new spawn
			return a.handleDenyResponse(ctx, tc)
		}
	}
	return nil
}

func (a *Actor) handleQuestionDenial(ctx context.Context, tc *taskContext, requestID string, toolInput json.RawMessage) error {
	var qPayload struct {
		Questions []struct {
			Question string `json:"question"`
			Options  []struct {
				Label string `json:"label"`
			} `json:"options"`
		} `json:"questions"`
	}
	_ = json.Unmarshal(toolInput, &qPayload)

	var questionText string
	var optionLabels []string
	if len(qPayload.Questions) > 0 {
		questionText = qPayload.Questions[0].Question
		for _, opt := range qPayload.Questions[0].Options {
			optionLabels = append(optionLabels, opt.Label)
		}
	}

	if err := a.opts.Relay.Send(&protocol.Question{
		Type:      protocol.MsgTypeQuestion,
		SessionID: a.opts.SessionID,
		ChannelID: a.opts.ChannelID,
		RequestID: requestID,
		Question:  questionText,
		Options:   optionLabels,
	}); err != nil {
		return err
	}
	if a.verbosity != display.Quiet {
		fmt.Printf("%s? %s%s\n", display.Cyan, questionText, display.Reset)
		for i, opt := range optionLabels {
			fmt.Printf("%s  %d) %s%s\n", display.Dim, i+1, opt, display.Reset)
		}
		fmt.Printf("%swaiting for answer...%s\n", display.Dim, display.Reset)
	}

	// Wait for answer
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.stopCh:
		return fmt.Errorf("actor stopped while waiting for answer")
	case resp := <-a.permCh:
		if a.verbosity != display.Quiet {
			fmt.Printf("\n%sAnswer: %s%s\n\n", display.Green, resp.Answer, display.Reset)
		}
		// Send answer as new prompt via --resume
		answerPrompt := fmt.Sprintf("My answer: %s", resp.Answer)
		return a.runExecutor(ctx, tc, answerPrompt)
	}
}

func (a *Actor) handleDenyResponse(ctx context.Context, tc *taskContext) error {
	denyPrompt := "The previous tool request was denied. Please continue without using that tool."
	return a.runExecutor(ctx, tc, denyPrompt)
}

// HandlePermissionResponse processes a permission response from the relay.
func (a *Actor) HandlePermissionResponse(resp *protocol.PermissionResponse) error {
	select {
	case a.permCh <- permResponse{Approved: resp.Approved, ToolName: ""}:
		return nil
	default:
		return fmt.Errorf("no pending permission request for session %s", a.opts.SessionID)
	}
}

// HandleQuestionResponse processes an answer to a question.
func (a *Actor) HandleQuestionResponse(resp *protocol.QuestionResponse) error {
	select {
	case a.permCh <- permResponse{Answer: resp.Answer}:
		return nil
	default:
		return fmt.Errorf("no pending question for session %s", a.opts.SessionID)
	}
}

func (a *Actor) addAllowedTool(name string) {
	for _, t := range a.allowedTools {
		if t == name {
			return
		}
	}
	a.allowedTools = append(a.allowedTools, name)
}

// GetClaudeSessionID returns the most recent Claude session id.
func (a *Actor) GetClaudeSessionID() string {
	return a.claudeSessionID
}

// Stop signals the actor to shut down.
func (a *Actor) Stop() error {
	select {
	case <-a.stopCh:
		// already closed
	default:
		close(a.stopCh)
	}
	return a.log.Close()
}

// PruneWAL removes WAL entries up to (and including) upTo.
func (a *Actor) PruneWAL(upTo int64) error {
	return a.log.PruneUpTo(upTo)
}

func toolInputSummary(raw json.RawMessage) (string, bool) {
	var input map[string]interface{}
	if json.Unmarshal(raw, &input) != nil {
		return "", false
	}
	if cmd, ok := input["command"].(string); ok {
		return "command: " + cmd, true
	}
	if fp, ok := input["file_path"].(string); ok {
		return "file_path: " + fp, true
	}
	return "", false
}
```

Key changes from old actor:
- No mutex, no atomic values — Run is the only goroutine that touches state
- `taskCh` and `permCh` channels replace direct method calls racing with executor
- `handleDenials` blocks on `permCh` then re-spawns — no `RestartWithGrant`
- `seq` is a plain int64 (only touched by Run goroutine)
- `taskContext` has no unused fields (Fix 2 from spec)
- Relay send failures are logged, not swallowed (Fix 1 from spec)

- [ ] **Step 2: Verify compilation (session tests won't pass yet)**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go build ./internal/session/`
Expected: May have compilation errors from test file — will fix in Task 5.

- [ ] **Step 3: Commit**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
git add internal/session/actor.go
git commit -m "feat(actor): rewrite for spawn-per-task with channel-based task delivery"
```

---

### Task 5: Update actor_test.go and manager.go

**Files:**
- Rewrite: `internal/session/actor_test.go`
- Update: `internal/session/manager.go`

Tests adapt to channel-based SendTask. Manager comments get updated.

- [ ] **Step 1: Update manager.go**

In `internal/session/manager.go`, update the Spawn goroutine. The comment about PTY/ready channel is stale. Replace the goroutine block (lines 88-118) with:

```go
	relay := opts.Relay
	sessionID := opts.SessionID
	channelID := opts.ChannelID
	go func() {
		err := actor.Run(ctx)
		if err == nil || ctx.Err() != nil {
			return
		}
		log.Printf("[session] actor.Run exited with error: session=%s err=%v", sessionID, err)
		if m.verbosity != display.Quiet {
			fmt.Print(display.FormatErrorBanner(err.Error()))
		}
		// In spawn-per-task, task errors are reported by the actor's Run
		// loop directly. This goroutine only catches unexpected Run exits.
		_ = relay
		_ = channelID
	}()
```

Also remove the long stale comment block above it (lines 72-88 starting with "Capture Run's exit reason...").

- [ ] **Step 2: Rewrite actor_test.go**

Replace the entire file with:

```go
package session

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	protocol "github.com/gsd-build/protocol-go"
)

func buildFakeClaude(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	daemonDir := filepath.Join(filepath.Dir(thisFile), "..", "..")
	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "fake-claude")
	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/fake-claude")
	cmd.Dir = daemonDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build fake-claude: %v\n%s", err, out)
	}
	return binPath
}

type fakeRelay struct {
	mu     sync.Mutex
	cond   *sync.Cond
	frames []any
}

func newFakeRelay() *fakeRelay {
	r := &fakeRelay{}
	r.cond = sync.NewCond(&r.mu)
	return r
}

func (r *fakeRelay) Send(msg any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, msg)
	r.cond.Broadcast()
	return nil
}

func (r *fakeRelay) GetFrames() []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]any, len(r.frames))
	copy(out, r.frames)
	return out
}

func (r *fakeRelay) waitFor(t *testing.T, timeout time.Duration, predicate func([]any) bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)

	r.mu.Lock()
	defer r.mu.Unlock()

	if predicate(r.frames) {
		return true
	}

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-time.After(timeout):
			r.mu.Lock()
			r.cond.Broadcast()
			r.mu.Unlock()
		case <-stop:
		}
	}()

	for !predicate(r.frames) {
		if time.Now().After(deadline) {
			return false
		}
		r.cond.Wait()
	}
	return true
}

func (r *fakeRelay) waitForTaskComplete(t *testing.T, timeout time.Duration) bool {
	return r.waitFor(t, timeout, func(frames []any) bool {
		for _, f := range frames {
			if _, ok := f.(*protocol.TaskComplete); ok {
				return true
			}
		}
		return false
	})
}

func TestActorHappyPath(t *testing.T) {
	binPath := buildFakeClaude(t)
	walDir := t.TempDir()
	relay := newFakeRelay()

	actor, err := NewActor(Options{
		SessionID:  "sess-1",
		ChannelID:  "ch-1",
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		WALPath:    filepath.Join(walDir, "sess-1.jsonl"),
		Relay:      relay,
		StartSeq:   0,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go func() { _ = actor.Run(ctx) }()

	if err := actor.SendTask(protocol.Task{
		TaskID:    "task-1",
		SessionID: "sess-1",
		ChannelID: "ch-1",
		Prompt:    "hello",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	if !relay.waitForTaskComplete(t, 10*time.Second) {
		t.Fatal("timed out waiting for TaskComplete frame")
	}
	_ = actor.Stop()

	// Verify relay received stream events with monotonic seqs
	frames := relay.GetFrames()
	var streamFrames []*protocol.Stream
	for _, f := range frames {
		if s, ok := f.(*protocol.Stream); ok {
			streamFrames = append(streamFrames, s)
		}
	}
	if len(streamFrames) < 2 {
		t.Fatalf("expected at least 2 stream frames, got %d", len(streamFrames))
	}

	var lastSeq int64
	for i, s := range streamFrames {
		if s.SequenceNumber <= lastSeq {
			t.Errorf("non-monotonic seq at %d: %d", i, s.SequenceNumber)
		}
		lastSeq = s.SequenceNumber
	}

	// Verify taskComplete with session id
	var completes []*protocol.TaskComplete
	for _, f := range frames {
		if tc, ok := f.(*protocol.TaskComplete); ok {
			completes = append(completes, tc)
		}
	}
	if len(completes) != 1 {
		t.Fatalf("expected 1 taskComplete, got %d", len(completes))
	}
	if completes[0].ClaudeSessionID != "fake-session-123" {
		t.Errorf("expected claudeSessionId=fake-session-123, got %s", completes[0].ClaudeSessionID)
	}
}

func TestActorPermissionDenialAndApproval(t *testing.T) {
	binPath := buildFakeClaude(t)
	walDir := t.TempDir()
	relay := newFakeRelay()

	t.Setenv("FAKE_CLAUDE_DENY_TOOL", "Write")

	actor, err := NewActor(Options{
		SessionID:  "sess-perm",
		ChannelID:  "ch-1",
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		WALPath:    filepath.Join(walDir, "sess-perm.jsonl"),
		Relay:      relay,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	go func() { _ = actor.Run(ctx) }()

	if err := actor.SendTask(protocol.Task{
		TaskID:    "task-1",
		SessionID: "sess-perm",
		ChannelID: "ch-1",
		Prompt:    "Write a file",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Wait for PermissionRequest
	gotPerm := relay.waitFor(t, 10*time.Second, func(frames []any) bool {
		for _, f := range frames {
			if _, ok := f.(*protocol.PermissionRequest); ok {
				return true
			}
		}
		return false
	})
	if !gotPerm {
		t.Fatal("timed out waiting for PermissionRequest")
	}

	// Approve
	if err := actor.HandlePermissionResponse(&protocol.PermissionResponse{
		Type:      protocol.MsgTypePermissionResponse,
		SessionID: "sess-perm",
		ChannelID: "ch-1",
		RequestID: "toolu_fake_001",
		Approved:  true,
	}); err != nil {
		t.Fatalf("HandlePermissionResponse: %v", err)
	}

	// Should get TaskComplete after re-spawn
	if !relay.waitForTaskComplete(t, 10*time.Second) {
		t.Fatal("timed out waiting for TaskComplete after approval")
	}
	_ = actor.Stop()

	// Verify allowed tools were updated
	allowed := actor.AllowedTools()
	if len(allowed) != 1 || allowed[0] != "Write" {
		t.Errorf("allowedTools: %+v", allowed)
	}
}

func TestActorSendTaskWhenBusy(t *testing.T) {
	// With a buffered(1) channel, the second SendTask should fail
	relay := newFakeRelay()
	tmpDir := t.TempDir()
	a, err := NewActor(Options{
		SessionID: "s-1",
		ChannelID: "c-1",
		WALPath:   filepath.Join(tmpDir, "s-1.jsonl"),
		Relay:     relay,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	defer a.Stop()

	// Fill the channel without starting Run
	_ = a.SendTask(protocol.Task{TaskID: "t1", Prompt: "first"})

	// Second should fail
	err = a.SendTask(protocol.Task{TaskID: "t2", Prompt: "second"})
	if err == nil {
		t.Fatal("expected error when task channel full")
	}
}
```

- [ ] **Step 3: Run session tests**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go test ./internal/session/ -v -count=1`
Expected: All PASS.

- [ ] **Step 4: Commit**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
git add internal/session/actor.go internal/session/actor_test.go internal/session/manager.go
git commit -m "feat(session): adapt actor tests and manager for spawn-per-task"
```

---

### Task 6: Bundled WAL and relay fixes

**Files:**
- Fix: `internal/wal/wal.go`
- Fix: `internal/wal/recover.go`
- Fix: `internal/relay/client.go`

These are independent of the executor/actor refactor.

- [ ] **Step 1: Fix WAL prune temp file cleanup**

In `internal/wal/wal.go`, in the `PruneUpTo` method, after the `os.Rename` call (line 150), add temp cleanup in the error path. Replace:

```go
	if err := os.Rename(tmp, l.path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
```

With:

```go
	if err := os.Rename(tmp, l.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
```

- [ ] **Step 2: Fix WAL ReadFrom-Prune race**

In `PruneUpTo`, the `ReadFrom` call is outside the lock. Move the lock acquisition to before `ReadFrom`. Replace the entire `PruneUpTo` method:

```go
// PruneUpTo rewrites the log with only entries where Seq > upTo.
func (l *Log) PruneUpTo(upTo int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Flush pending writes before reading
	if err := l.w.Flush(); err != nil {
		return err
	}

	// Read all entries from the file
	f, err := os.Open(l.path)
	if err != nil {
		return fmt.Errorf("open for read: %w", err)
	}

	var remaining []Entry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			f.Close()
			return fmt.Errorf("parse entry: %w", err)
		}
		if e.Seq > upTo {
			remaining = append(remaining, e)
		}
	}
	if err := scanner.Err(); err != nil {
		f.Close()
		return fmt.Errorf("scan: %w", err)
	}
	f.Close()

	// Write remaining to temp file
	tmp := l.path + ".tmp"
	tf, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("open tmp: %w", err)
	}
	w := bufio.NewWriter(tf)
	for _, e := range remaining {
		line, _ := json.Marshal(e)
		line = append(line, '\n')
		if _, err := w.Write(line); err != nil {
			tf.Close()
			os.Remove(tmp)
			return err
		}
	}
	if err := w.Flush(); err != nil {
		tf.Close()
		os.Remove(tmp)
		return err
	}
	if err := tf.Sync(); err != nil {
		tf.Close()
		os.Remove(tmp)
		return err
	}
	if err := tf.Close(); err != nil {
		os.Remove(tmp)
		return err
	}

	// Swap atomically
	_ = l.w.Flush()
	_ = l.f.Close()
	if err := os.Rename(tmp, l.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	nf, err := os.OpenFile(l.path, os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("reopen: %w", err)
	}
	l.f = nf
	l.w = bufio.NewWriter(nf)
	return nil
}
```

- [ ] **Step 3: Fix WAL scan logging for corrupt files**

In `internal/wal/recover.go`, add `"log"` to imports and replace the silent `continue` lines:

Replace:
```go
		log, err := Open(filepath.Join(walDir, name))
		if err != nil {
			continue
		}
		walEntries, err := log.ReadFrom(0)
		_ = log.Close()
		if err != nil || len(walEntries) == 0 {
			continue
		}
```

With:
```go
		walLog, err := Open(filepath.Join(walDir, name))
		if err != nil {
			log.Printf("[wal] warning: cannot open WAL file %s: %v", name, err)
			continue
		}
		walEntries, err := walLog.ReadFrom(0)
		_ = walLog.Close()
		if err != nil {
			log.Printf("[wal] warning: cannot read WAL file %s: %v", name, err)
			continue
		}
		if len(walEntries) == 0 {
			continue
		}
```

Note: rename the variable from `log` to `walLog` to avoid shadowing the `log` package import.

- [ ] **Step 4: Fix relay Welcome timeout**

In `internal/relay/client.go`, in the `Connect` method, replace line 96:

```go
	_, data, err := conn.Read(ctx)
```

With:
```go
	welcomeCtx, welcomeCancel := context.WithTimeout(ctx, 10*time.Second)
	defer welcomeCancel()
	_, data, err := conn.Read(welcomeCtx)
```

- [ ] **Step 5: Run WAL and relay tests**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go test ./internal/wal/ ./internal/relay/ -v -count=1`
Expected: All PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
git add internal/wal/wal.go internal/wal/recover.go internal/relay/client.go
git commit -m "fix: WAL prune cleanup, ReadFrom-Prune race, scan logging, relay Welcome timeout"
```

---

### Task 7: Update e2e tests and remove unused dependencies

**Files:**
- Update: `tests/e2e/daemon_e2e_test.go`
- Update: `go.mod`

- [ ] **Step 1: Run e2e tests to see what breaks**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go test ./tests/e2e/ -v -count=1`
Expected: Should pass if fake-claude and actor changes are compatible. If not, note the errors.

- [ ] **Step 2: Fix any e2e test failures**

The e2e test sends a Task via the stub relay. The daemon's `handleTask` calls `actor.SendTask()` which now writes to the task channel. The actor's `Run` loop picks it up and spawns fake-claude. fake-claude reads the prompt from argv, emits events, exits. Events flow through display + WAL + relay back to the stub.

If tests fail, likely causes:
- Timing: the actor's Run loop needs to be started before SendTask is called. Check that `manager.Spawn` starts the goroutine.
- fake-claude args: verify the prompt is the last positional arg.

Fix whatever breaks.

- [ ] **Step 3: Remove unused dependencies**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go mod tidy`

This should remove `creack/pty` and `golang.org/x/term` from go.mod since nothing imports them anymore (pty_unix.go is deleted, fake-claude no longer imports term).

Verify: `grep -E "creack/pty|golang.org/x/term" go.mod` should return nothing.

- [ ] **Step 4: Run full test suite**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go test ./... -count=1`
Expected: All PASS.

- [ ] **Step 5: Run go vet**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go vet ./...`
Expected: No issues.

- [ ] **Step 6: Run go build**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go build ./...`
Expected: Clean build.

- [ ] **Step 7: Commit**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
git add go.mod go.sum tests/e2e/
git commit -m "chore: update e2e tests, remove creack/pty and x/term dependencies"
```
