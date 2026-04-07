package claude

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
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
