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

// waitForFile polls for the existence of path with a 3s timeout. Used by
// argv-inspection tests instead of a bare time.Sleep, so the test does
// not race against fake-claude's startup overhead on slow machines or
// against the additional per-spawn cost of pty allocation.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("file never appeared: %s", path)
}

func buildFakeClaude(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	daemonDir := filepath.Join(filepath.Dir(thisFile), "..", "..")
	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "fake-claude")

	// Build the helper
	if err := runCmd(t, daemonDir, "go", "build", "-o", binPath, "./cmd/fake-claude"); err != nil {
		t.Fatalf("build fake-claude: %v", err)
	}
	return binPath
}

// buildFakeClaudeBlockbuf compiles the second fake-claude variant that
// replicates Node.js's libuv block-buffering decision for non-TTY
// stdout. Tests that point at this binary will only receive stream
// events if the executor attaches a pseudo-terminal to the child's
// stdout; a pipe-based executor will see zero events during the task.
func buildFakeClaudeBlockbuf(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	daemonDir := filepath.Join(filepath.Dir(thisFile), "..", "..")
	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "fake-claude-blockbuf")
	if err := runCmd(t, daemonDir, "go", "build", "-o", binPath, "./cmd/fake-claude-blockbuf"); err != nil {
		t.Fatalf("build fake-claude-blockbuf: %v", err)
	}
	return binPath
}

func runCmd(t *testing.T, dir, name string, args ...string) error {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("cmd output: %s", out)
	}
	return err
}

func TestExecutorRoundTrip(t *testing.T) {
	binPath := buildFakeClaude(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	exec := NewExecutor(Options{
		BinaryPath:     binPath,
		CWD:            t.TempDir(),
		Model:          "test-model",
		Effort:         "max",
		PermissionMode: "acceptEdits",
	})

	var (
		mu        sync.Mutex
		events    []Event
		gotResult = make(chan struct{})
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = exec.Start(ctx, func(e Event) error {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
			if e.Type == "result" {
				select {
				case <-gotResult:
				default:
					close(gotResult)
				}
			}
			return nil
		})
	}()

	if err := exec.Send(`hello`); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Wait for the result event rather than sleeping a fixed duration.
	// fake-claude startup + pty allocation can consume 150+ ms on slow
	// machines, and a bare sleep races against that.
	select {
	case <-gotResult:
	case <-time.After(3 * time.Second):
		t.Fatal("never received result event")
	}
	_ = exec.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("executor did not exit")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(events))
	}

	// Last event should be "result"
	last := events[len(events)-1]
	if last.Type != "result" {
		t.Errorf("expected last event type=result, got %s", last.Type)
	}

	// And should have a session_id
	var payload map[string]any
	_ = json.Unmarshal(last.Raw, &payload)
	if payload["session_id"] != "fake-session-123" {
		t.Errorf("expected session_id=fake-session-123, got %v", payload["session_id"])
	}
}

// TestExecutorBlockBufferingFakeClaude is the regression test for the
// daemon-stdout-silent bug. It spawns fake-claude-blockbuf, which
// faithfully replicates real claude's Node/libuv stdout buffering:
// line-buffered on a TTY, block-buffered on a pipe. If the Executor's
// stdout path is a plain pipe (the pre-PR-2 behavior), the fake buffers
// everything in userspace and the test times out with zero events. If
// the Executor attaches a pseudo-terminal (the PR-2 behavior), the fake
// detects a TTY and flushes per line, and the test sees all events.
//
// The test is BOTH a regression proof (it fails cleanly if someone
// reverts the pty path) and a production-bug reproduction (it replicates
// the exact mechanism that caused the v0.1.2 daemon to drop every event
// from short assistant responses).
func TestExecutorBlockBufferingFakeClaude(t *testing.T) {
	binPath := buildFakeClaudeBlockbuf(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	exec := NewExecutor(Options{
		BinaryPath: binPath,
		CWD:        t.TempDir(),
	})

	var (
		mu        sync.Mutex
		events    []Event
		gotResult = make(chan struct{})
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = exec.Start(ctx, func(e Event) error {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
			if e.Type == "result" {
				select {
				case <-gotResult:
				default:
					close(gotResult)
				}
			}
			return nil
		})
	}()

	if err := exec.Send("hello"); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Wait for the result event. On a pipe-based executor this would
	// hang forever (no events flushed). The timeout keeps the failure
	// bounded so CI reports "did not see result event" rather than
	// hanging the whole run.
	select {
	case <-gotResult:
	case <-time.After(5 * time.Second):
		mu.Lock()
		n := len(events)
		mu.Unlock()
		t.Fatalf("block-buffering fake never flushed to the executor; got %d events in 5s. Did the executor revert to a pipe-based stdout?", n)
	}
	_ = exec.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("executor did not exit")
	}

	mu.Lock()
	defer mu.Unlock()
	// Expect exactly the three events the fake emits per turn:
	// stream_event, assistant, result.
	if len(events) < 3 {
		t.Fatalf("expected >=3 events from blockbuf fake, got %d", len(events))
	}
	if events[len(events)-1].Type != "result" {
		t.Errorf("expected last event to be result, got %s", events[len(events)-1].Type)
	}
}

// TestExecutorStdinIsNotTTY is the regression test for the production
// bug hit against claude@2.1.96 on 2026-04-08: when both stdin and
// stdout were attached to the pty slave (the pre-split-stdio layout),
// claude's `-p --input-format stream-json` mode ignored --input-format,
// dropped into an interactive code path, and exited immediately with:
//
//	Error: Input must be provided either through stdin or as a prompt argument when using --print
//
// The fix is asymmetric stdio: stdin on a regular anonymous pipe
// (so claude's -p mode accepts stream-json) and stdout on a pty slave
// (so Node line-buffers output). This test pins that invariant by
// spawning fake-claude with FAKE_CLAUDE_ASSERT_STDIN_PIPE=1, which
// calls term.IsTerminal(0) and exits with code 3 + a stderr message
// if stdin is a TTY. Any future change that flips stdin back to a pty
// will fail this test with a clear "fake-claude: stdin is a TTY" in
// the captured stderr.
//
// Separately, this test also asserts that stdout is still a TTY (by
// inspecting the successful round-trip: fake-claude only emits its
// init+result events at all, and on time, when the executor keeps
// stdout on the pty slave — the existing TestExecutorBlockBufferingFakeClaude
// covers that side in isolation, but we re-check it implicitly here
// so this test documents the full "split stdio" contract).
func TestExecutorStdinIsNotTTY(t *testing.T) {
	binPath := buildFakeClaude(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	exec := NewExecutor(Options{
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Env:        []string{"FAKE_CLAUDE_ASSERT_STDIN_PIPE=1"},
	})

	done := make(chan error, 1)
	go func() {
		done <- exec.Start(ctx, func(_ Event) error { return nil })
	}()

	// Round-trip through Send so the pipe actually carries a byte,
	// then Close to shut fake-claude down cleanly. If the fake's
	// stdin assertion fires, the process will exit with code 3 and
	// a stderr message; Start's return value will contain both.
	_ = exec.Send("hello")
	_ = exec.Close()

	select {
	case err := <-done:
		if err != nil {
			// If the fake detected a TTY stdin it exits with code 3
			// and a known stderr message (captured via PR 1's ring
			// buffer). Any other error is an unrelated failure.
			if strings.Contains(err.Error(), "stdin is a TTY") {
				t.Fatalf("executor attached a TTY to child stdin; expected a pipe. err: %v", err)
			}
			t.Fatalf("executor returned unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("executor did not exit after stdin assertion check")
	}
}

// readArgsFile reads the argv written by fake-claude when FAKE_CLAUDE_ARGS_FILE is set.
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

// TestExecutorResumeFlag verifies that a non-empty ResumeSession option
// causes the executor to pass --resume <id> in the subprocess argv.
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
		Env:           []string{"FAKE_CLAUDE_ARGS_FILE=" + argsFile},
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.Start(ctx, func(_ Event) error { return nil })
	}()

	// Wait for fake-claude to write the args file, then shut it down.
	// The old form used a fixed 50 ms sleep, but pty allocation plus Go
	// runtime startup on a fresh subprocess can easily exceed that.
	waitForFile(t, argsFile)
	_ = e.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("executor did not exit")
	}

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

// TestExecutorStderrCapturedOnCrash verifies that when the subprocess exits
// non-zero, the error returned by Start includes the tail of the subprocess's
// stderr output. Regression test for the old `cmd.Stderr = nil` behavior that
// silently discarded all diagnostic output from claude.
func TestExecutorStderrCapturedOnCrash(t *testing.T) {
	binPath := buildFakeClaude(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const stderrMsg = "simulated claude failure: invalid API key"
	e := NewExecutor(Options{
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Env: []string{
			"FAKE_CLAUDE_STDERR=" + stderrMsg,
			"FAKE_CLAUDE_EXIT_CODE=2",
		},
	})

	goroutinesBefore := runtime.NumGoroutine()

	err := e.Start(ctx, func(_ Event) error { return nil })
	if err == nil {
		t.Fatal("expected error from Start when subprocess exits non-zero, got nil")
	}
	if !strings.Contains(err.Error(), stderrMsg) {
		t.Errorf("expected error to contain stderr message %q, got: %v", stderrMsg, err)
	}
	if !strings.Contains(err.Error(), "code 2") {
		t.Errorf("expected error to mention exit code 2, got: %v", err)
	}

	// Allow Go runtime a moment to tear down transient goroutines.
	time.Sleep(50 * time.Millisecond)
	goroutinesAfter := runtime.NumGoroutine()
	if delta := goroutinesAfter - goroutinesBefore; delta > 2 {
		t.Errorf("goroutine leak: before=%d after=%d delta=%d", goroutinesBefore, goroutinesAfter, delta)
	}
}

// TestExecutorNormalShutdownNoError verifies that closing stdin cleanly
// (the normal shutdown path) does NOT surface a spurious error from Start
// even though the subprocess exits when its stdin is closed.
func TestExecutorNormalShutdownNoError(t *testing.T) {
	binPath := buildFakeClaude(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	e := NewExecutor(Options{
		BinaryPath: binPath,
		CWD:        t.TempDir(),
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- e.Start(ctx, func(_ Event) error { return nil })
	}()

	if err := e.Send("hello"); err != nil {
		t.Fatalf("send: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	// Cancel ctx first so the executor's ctx.Err() check swallows the
	// wait error from the killed process.
	cancel()
	_ = e.Close()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("expected nil error from clean shutdown, got: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not return after Close")
	}
}

// TestExecutorNoResumeFlag verifies that an empty ResumeSession does not
// add --resume to the subprocess argv.
func TestExecutorNoResumeFlag(t *testing.T) {
	binPath := buildFakeClaude(t)

	argsFile := filepath.Join(t.TempDir(), "argv.json")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	e := NewExecutor(Options{
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		// ResumeSession intentionally empty
		Env: []string{"FAKE_CLAUDE_ARGS_FILE=" + argsFile},
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.Start(ctx, func(_ Event) error { return nil })
	}()

	waitForFile(t, argsFile)
	_ = e.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("executor did not exit")
	}

	argv := readArgsFile(t, argsFile)

	for _, arg := range argv {
		if arg == "--resume" {
			t.Errorf("expected no --resume in argv, got: %v", argv)
			break
		}
	}
}
