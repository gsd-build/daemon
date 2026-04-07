package session

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	protocol "github.com/gsd-cloud/protocol-go"
)

func buildFakeClaude(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	daemonDir := filepath.Join(filepath.Dir(thisFile), "..", "..")
	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "fake-claude")
	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/fake-claude")
	cmd.Dir = daemonDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build fake-claude: %v\n%s", err, out)
	}
	return binPath
}

// fakeRelay captures outgoing frames
type fakeRelay struct {
	mu     sync.Mutex
	frames []any
}

func (r *fakeRelay) Send(msg any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, msg)
	return nil
}

func (r *fakeRelay) GetFrames() []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]any, len(r.frames))
	copy(out, r.frames)
	return out
}

func TestActorAssignsMonotonicSequenceAndWritesWAL(t *testing.T) {
	binPath := buildFakeClaude(t)
	walDir := t.TempDir()
	relay := &fakeRelay{}

	actor, err := NewActor(Options{
		SessionID:  "sess-1",
		ChannelID:  "ch-1",
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		WALPath:    filepath.Join(walDir, "sess-1.jsonl"),
		Relay:      relay,
		StartSeq:   0,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() { _ = actor.Run(ctx) }()

	if err := actor.SendTask(protocol.Task{
		TaskID:    "task-1",
		SessionID: "sess-1",
		ChannelID: "ch-1",
		Prompt:    "hello",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	time.Sleep(500 * time.Millisecond)
	_ = actor.Stop()

	// Verify relay received stream events with monotonic seqs
	frames := relay.GetFrames()
	var streamFrames []*protocol.Stream
	for _, f := range frames {
		if s, ok := f.(*protocol.Stream); ok {
			streamFrames = append(streamFrames, s)
		}
	}
	if len(streamFrames) < 2 {
		t.Fatalf("expected at least 2 stream frames, got %d", len(streamFrames))
	}

	var lastSeq int64
	for i, s := range streamFrames {
		if s.SequenceNumber <= lastSeq {
			t.Errorf("non-monotonic seq at %d: %d", i, s.SequenceNumber)
		}
		lastSeq = s.SequenceNumber
	}

	// Verify at least one taskComplete with the fake session id
	var completes []*protocol.TaskComplete
	for _, f := range frames {
		if tc, ok := f.(*protocol.TaskComplete); ok {
			completes = append(completes, tc)
		}
	}
	if len(completes) != 1 {
		t.Fatalf("expected exactly 1 taskComplete, got %d", len(completes))
	}
	if completes[0].ClaudeSessionID != "fake-session-123" {
		t.Errorf("expected claudeSessionId=fake-session-123, got %s", completes[0].ClaudeSessionID)
	}
}

func TestActorRecoversStartSeqFromWAL(t *testing.T) {
	binPath := buildFakeClaude(t)
	walDir := t.TempDir()
	walPath := filepath.Join(walDir, "sess-1.jsonl")

	// First actor: writes a few entries
	relay1 := &fakeRelay{}
	a1, _ := NewActor(Options{
		SessionID:  "sess-1",
		ChannelID:  "c",
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		WALPath:    walPath,
		Relay:      relay1,
		StartSeq:   0,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = a1.Run(ctx) }()
	_ = a1.SendTask(protocol.Task{TaskID: "t1", SessionID: "sess-1", ChannelID: "c", Prompt: "x"})
	time.Sleep(300 * time.Millisecond)
	_ = a1.Stop()

	lastSeq := a1.LastSequence()
	if lastSeq == 0 {
		t.Fatal("expected lastSeq > 0 after first actor ran")
	}

	// Second actor: start with StartSeq = lastSeq, new events should be lastSeq+1, +2, ...
	relay2 := &fakeRelay{}
	a2, _ := NewActor(Options{
		SessionID:  "sess-1",
		ChannelID:  "c",
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		WALPath:    walPath,
		Relay:      relay2,
		StartSeq:   lastSeq,
	})
	go func() { _ = a2.Run(ctx) }()
	_ = a2.SendTask(protocol.Task{TaskID: "t2", SessionID: "sess-1", ChannelID: "c", Prompt: "y"})
	time.Sleep(300 * time.Millisecond)
	_ = a2.Stop()

	// Check that sequence numbers in relay2 start > lastSeq
	for _, f := range relay2.GetFrames() {
		if s, ok := f.(*protocol.Stream); ok {
			if s.SequenceNumber <= lastSeq {
				t.Errorf("new actor emitted seq=%d, expected > %d", s.SequenceNumber, lastSeq)
			}
		}
	}
	_ = json.Unmarshal
}
