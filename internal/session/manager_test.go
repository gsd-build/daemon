package session

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gsd-build/daemon/internal/config"
)

type nullRelay struct{}

func (nullRelay) Send(ctx context.Context, msg any) error { return nil }

func TestManagerSpawnAndGet(t *testing.T) {
	binPath := buildFakeClaude(t)
	cfg := &config.Config{MaxConcurrentTasks: 10}
	m := NewManager(ManagerOptions{
		BinaryPath: binPath,
		Relay:      nullRelay{},
		Config:     cfg,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	a, err := m.Spawn(ctx, Options{
		SessionID: "s-1",
		CWD:       t.TempDir(),
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if a == nil {
		t.Fatal("actor should not be nil")
	}

	got := m.Get("s-1")
	if got != a {
		t.Errorf("expected same actor instance")
	}

	// Second Spawn returns existing actor
	a2, _ := m.Spawn(ctx, Options{SessionID: "s-1", CWD: t.TempDir()})
	if a2 != a {
		t.Errorf("expected idempotent spawn")
	}

	m.StopAll()
	if m.Get("s-1") != nil {
		t.Errorf("expected actor cleared after StopAll")
	}
}

func TestManagerRejectAtCapacity(t *testing.T) {
	relay := newFakeRelay()
	cfg := &config.Config{MaxConcurrentTasks: 1}

	mgr := NewManager(ManagerOptions{
		BinaryPath: "fake",
		Relay:      relay,
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
