package session

import (
	"context"
	"testing"
	"time"

	"github.com/gsd-build/daemon/internal/display"
)

type nullRelay struct{}

func (nullRelay) Send(ctx context.Context, msg any) error { return nil }

func TestManagerSpawnAndGet(t *testing.T) {
	binPath := buildFakeClaude(t)
	m := NewManager(binPath, nullRelay{}, display.Quiet)

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

