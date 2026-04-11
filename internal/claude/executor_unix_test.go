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
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()

	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("getpgid: %v", err)
	}
	if pgid != cmd.Process.Pid {
		t.Errorf("expected pgid=%d (own group), got pgid=%d", cmd.Process.Pid, pgid)
	}
}

func TestPIDCallbackInvoked(t *testing.T) {
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
