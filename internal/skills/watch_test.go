package skills

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatcherDebouncesBurstChanges(t *testing.T) {
	root := t.TempDir()
	writeSkillFixture(t, root, "watched-skill", "Watched skill")

	var calls atomic.Int32
	watcher, err := NewWatcher(WatchOptions{
		Debounce: 75 * time.Millisecond,
		OnChange: func() { calls.Add(1) },
	})
	if err != nil {
		t.Fatalf("new watcher: %v", err)
	}
	defer watcher.Close()

	if err := watcher.AddRoot(root); err != nil {
		t.Fatalf("add root: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = watcher.Run(ctx) }()

	skillFile := filepath.Join(root, "watched-skill", "SKILL.md")
	if err := os.WriteFile(skillFile, []byte("one"), 0o644); err != nil {
		t.Fatalf("write first: %v", err)
	}
	if err := os.WriteFile(skillFile, []byte("two"), 0o644); err != nil {
		t.Fatalf("write second: %v", err)
	}

	waitForWatcherCalls(t, &calls, 1, time.Second)
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected 1 debounced callback, got %d", got)
	}
}

func TestWatcherSuppressesSelfWrites(t *testing.T) {
	root := t.TempDir()
	writeSkillFixture(t, root, "managed-skill", "Managed skill")

	var calls atomic.Int32
	watcher, err := NewWatcher(WatchOptions{
		Debounce: 75 * time.Millisecond,
		OnChange: func() { calls.Add(1) },
	})
	if err != nil {
		t.Fatalf("new watcher: %v", err)
	}
	defer watcher.Close()

	if err := watcher.AddRoot(root); err != nil {
		t.Fatalf("add root: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = watcher.Run(ctx) }()

	skillDir := filepath.Join(root, "managed-skill")
	watcher.SuppressPath(skillDir, 500*time.Millisecond)

	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("updated"), 0o644); err != nil {
		t.Fatalf("write managed file: %v", err)
	}

	time.Sleep(300 * time.Millisecond)
	if got := calls.Load(); got != 0 {
		t.Fatalf("expected suppressed writes to avoid callbacks, got %d", got)
	}
}

func waitForWatcherCalls(t *testing.T, calls *atomic.Int32, want int32, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if calls.Load() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for watcher callbacks: got %d want %d", calls.Load(), want)
}
