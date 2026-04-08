package claude

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

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

	var events []Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = exec.Start(ctx, func(e Event) error {
			events = append(events, e)
			return nil
		})
	}()

	if err := exec.Send(`hello`); err != nil {
		t.Fatalf("send: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	_ = exec.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("executor did not exit")
	}

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

	// Give fake-claude time to write the args file, then close it.
	time.Sleep(50 * time.Millisecond)
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

	time.Sleep(50 * time.Millisecond)
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
