package session

import (
	"context"
	"testing"
	"time"
)

type nullRelay struct{}

func (nullRelay) Send(msg any) error { return nil }

func TestManagerSpawnAndGet(t *testing.T) {
	binPath := buildFakeClaude(t)
	m := NewManager(t.TempDir(), binPath, nullRelay{})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	a, err := m.Spawn(ctx, Options{
		SessionID: "s-1",
		ChannelID: "c",
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

func TestLastSequencesSnapshot(t *testing.T) {
	binPath := buildFakeClaude(t)
	m := NewManager(t.TempDir(), binPath, nullRelay{})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, _ = m.Spawn(ctx, Options{SessionID: "a", CWD: t.TempDir()})
	_, _ = m.Spawn(ctx, Options{SessionID: "b", CWD: t.TempDir()})

	snap := m.LastSequences()
	if _, ok := snap["a"]; !ok {
		t.Errorf("missing a in snapshot")
	}
	if _, ok := snap["b"]; !ok {
		t.Errorf("missing b in snapshot")
	}
	m.StopAll()
}
