package pi

import (
	"os/exec"
	"runtime"
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
