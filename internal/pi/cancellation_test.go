// Integration test for process cleanup during forced cancellation.
//
// Run manually:
//   go test ./internal/pi -tags=integration -v -run TestPiExecutor_NoOrphansOnCancellation
//
// Strategy: snapshot pi/claude PIDs before, run the executor with a context
// that cancels mid-task, sleep to let the OS reap, snapshot again. Assert no
// new PIDs appeared.

//go:build integration

package pi

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gsd-build/daemon/internal/claude"
)

func snapshotPiClaudePids(t *testing.T) map[string]bool {
	t.Helper()
	out, _ := exec.Command("pgrep", "-af", "pi |claude").Output()
	set := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, "pgrep") {
			continue
		}
		// Skip the `go test` framework's own subprocesses (compiler, linker,
		// test binary builds) which can match "pi " or "claude" by accident
		// when their tmp paths happen to contain those substrings.
		if strings.Contains(line, "go build") || strings.Contains(line, "go test") || strings.Contains(line, "compile -") || strings.Contains(line, "link -") {
			continue
		}
		pid := strings.SplitN(line, " ", 2)[0]
		set[pid] = true
	}
	return set
}

func TestPiExecutor_NoOrphansOnCancellation(t *testing.T) {
	if _, err := exec.LookPath("pi"); err != nil {
		t.Skip("pi binary not on PATH; skipping integration test")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude binary not on PATH; skipping integration test")
	}

	_, thisFile, _, _ := runtime.Caller(0)
	pkgDir := filepath.Dir(thisFile)
	extPath := filepath.Join(pkgDir, "extension", "index.ts")

	before := snapshotPiClaudePids(t)
	t.Logf("baseline pi/claude PIDs: %d", len(before))

	e := NewExecutor(Options{
		CWD:           t.TempDir(),
		Model:         "claude-sonnet-4-6",
		Provider:      "claude-cli",
		ExtensionPath: extPath,
		// Long-running prompt so cancellation lands mid-task.
		Prompt: "Count from 1 to 50 slowly, one number per line, with a one-sentence reflection after each number.",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	_ = e.Run(ctx, func(ev claude.Event) error { return nil }, nil)
	t.Logf("Run returned after %s (ctx err: %v)", time.Since(start), ctx.Err())

	// Let the OS reap the process group. macOS process accounting can lag
	// up to ~500ms; 3s is generous.
	time.Sleep(3 * time.Second)

	after := snapshotPiClaudePids(t)
	t.Logf("post-cancel pi/claude PIDs: %d", len(after))

	var orphans []string
	for pid := range after {
		if !before[pid] {
			orphans = append(orphans, pid)
		}
	}

	if len(orphans) > 0 {
		// Re-fetch full process info for each orphan for the failure message.
		for _, pid := range orphans {
			out, _ := exec.Command("ps", "-o", "pid,ppid,command", "-p", pid).Output()
			t.Errorf("orphan PID %s after cancellation:\n%s", pid, string(out))
		}
	}
}
