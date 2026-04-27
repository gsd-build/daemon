package pi

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTerminateProcessGroupAndWaitEscalates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process group signals are unix-specific")
	}

	cmd := exec.Command("sh", "-c", "trap '' TERM; sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start command: %v", err)
	}

	start := time.Now()
	err := terminateProcessGroupAndWait(cmd, cmd.Process.Pid, nil, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected killed process error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("terminate took %s", elapsed)
	}
}

func TestExecutorRequiresExistingExtension(t *testing.T) {
	exec := NewExecutor(Options{
		BinaryPath:    "definitely-not-a-real-pi-binary",
		CWD:           t.TempDir(),
		ExtensionPath: t.TempDir() + "/missing/index.ts",
		Prompt:        "hello",
	})

	err := exec.Run(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected missing extension error")
	}
	if !strings.Contains(err.Error(), "pi extension not found") {
		t.Fatalf("expected missing extension error, got %v", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
}
