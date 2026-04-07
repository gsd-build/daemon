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

// fakeRelay captures outgoing frames. It supports condition-based waiting so
// tests can block until a predicate over the captured frames is satisfied,
// instead of guessing at timing with time.Sleep.
type fakeRelay struct {
	mu     sync.Mutex
	cond   *sync.Cond
	frames []any
}

func newFakeRelay() *fakeRelay {
	r := &fakeRelay{}
	r.cond = sync.NewCond(&r.mu)
	return r
}

func (r *fakeRelay) Send(msg any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, msg)
	if r.cond != nil {
		r.cond.Broadcast()
	}
	return nil
}

func (r *fakeRelay) GetFrames() []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]any, len(r.frames))
	copy(out, r.frames)
	return out
}

// waitFor blocks until predicate(frames) returns true or timeout elapses.
// The predicate is called under the relay lock with the live frames slice;
// it must not mutate. Returns true if the condition was satisfied.
func (r *fakeRelay) waitFor(t *testing.T, timeout time.Duration, predicate func([]any) bool) bool {
	t.Helper()
	if r.cond == nil {
		r.cond = sync.NewCond(&r.mu)
	}
	deadline := time.Now().Add(timeout)

	r.mu.Lock()
	defer r.mu.Unlock()

	if predicate(r.frames) {
		return true
	}

	// Spawn a watchdog goroutine that broadcasts when the deadline passes,
	// so cond.Wait unblocks even if no further Sends happen.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-time.After(timeout):
			r.mu.Lock()
			r.cond.Broadcast()
			r.mu.Unlock()
		case <-stop:
		}
	}()

	for !predicate(r.frames) {
		if time.Now().After(deadline) {
			return false
		}
		r.cond.Wait()
	}
	return true
}

// waitForTaskComplete blocks until at least one TaskComplete frame is observed.
func (r *fakeRelay) waitForTaskComplete(t *testing.T, timeout time.Duration) bool {
	return r.waitFor(t, timeout, func(frames []any) bool {
		for _, f := range frames {
			if _, ok := f.(*protocol.TaskComplete); ok {
				return true
			}
		}
		return false
	})
}

func TestActorAssignsMonotonicSequenceAndWritesWAL(t *testing.T) {
	binPath := buildFakeClaude(t)
	walDir := t.TempDir()
	relay := newFakeRelay()

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

	// Wait for the full pipeline (subprocess spawn → fake-claude emits 3 events
	// → parser → handleEvent → relay) to land a TaskComplete frame, instead of
	// guessing with time.Sleep. Under -race the spawn alone can exceed 500ms.
	if !relay.waitForTaskComplete(t, 5*time.Second) {
		t.Fatal("timed out waiting for TaskComplete frame")
	}
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
	relay1 := newFakeRelay()
	a1, _ := NewActor(Options{
		SessionID:  "sess-1",
		ChannelID:  "c",
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		WALPath:    walPath,
		Relay:      relay1,
		StartSeq:   0,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = a1.Run(ctx) }()
	_ = a1.SendTask(protocol.Task{TaskID: "t1", SessionID: "sess-1", ChannelID: "c", Prompt: "x"})
	if !relay1.waitForTaskComplete(t, 5*time.Second) {
		t.Fatal("timed out waiting for first actor TaskComplete")
	}
	_ = a1.Stop()

	lastSeq := a1.LastSequence()
	if lastSeq == 0 {
		t.Fatal("expected lastSeq > 0 after first actor ran")
	}

	// Second actor: start with StartSeq = lastSeq, new events should be lastSeq+1, +2, ...
	relay2 := newFakeRelay()
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
	if !relay2.waitForTaskComplete(t, 5*time.Second) {
		t.Fatal("timed out waiting for second actor TaskComplete")
	}
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

func TestActorSynthesizesPermissionRequestFromResultDenial(t *testing.T) {
	relay := &fakeRelay{}
	tmpDir := t.TempDir()
	a, err := NewActor(Options{
		SessionID: "s-1",
		ChannelID: "c-1",
		WALPath:   filepath.Join(tmpDir, "s-1.jsonl"),
		Relay:     relay,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	defer a.Stop()

	a.taskInFlight.Store(&taskContext{
		TaskID:         "task-1",
		StartedAt:      time.Now(),
		OriginalPrompt: "Write hello.txt",
	})

	resultRaw := []byte(`{
		"type": "result",
		"subtype": "success",
		"session_id": "claude-abc",
		"total_cost_usd": 0.01,
		"duration_ms": 1000,
		"usage": {"input_tokens": 100, "output_tokens": 50},
		"permission_denials": [
			{
				"tool_name": "Write",
				"tool_use_id": "toolu_001",
				"tool_input": {"file_path": "/tmp/hello.txt", "content": "hi"}
			}
		]
	}`)

	if err := a.handleResult(resultRaw); err != nil {
		t.Fatalf("handleResult: %v", err)
	}

	frames := relay.GetFrames()
	var permReqs []*protocol.PermissionRequest
	var completes []*protocol.TaskComplete
	for _, f := range frames {
		switch v := f.(type) {
		case *protocol.PermissionRequest:
			permReqs = append(permReqs, v)
		case *protocol.TaskComplete:
			completes = append(completes, v)
		}
	}

	if len(permReqs) != 1 {
		t.Fatalf("expected 1 PermissionRequest, got %d", len(permReqs))
	}
	if permReqs[0].ToolName != "Write" {
		t.Errorf("tool name: %s", permReqs[0].ToolName)
	}
	if permReqs[0].RequestID != "toolu_001" {
		t.Errorf("request id: %s", permReqs[0].RequestID)
	}
	if len(completes) != 0 {
		t.Errorf("expected 0 TaskComplete (still waiting), got %d", len(completes))
	}

	// Verify pendingDenial was set
	pd, ok := a.pendingDenial.Load().(*pendingDenial)
	if !ok || pd == nil {
		t.Fatal("expected pendingDenial to be set")
	}
	if pd.TaskID != "task-1" {
		t.Errorf("pendingDenial.TaskID: %s", pd.TaskID)
	}
}

func TestActorSynthesizesQuestionFromAskUserQuestionDenial(t *testing.T) {
	relay := &fakeRelay{}
	tmpDir := t.TempDir()
	a, _ := NewActor(Options{
		SessionID: "s-1",
		ChannelID: "c-1",
		WALPath:   filepath.Join(tmpDir, "s-1.jsonl"),
		Relay:     relay,
	})
	defer a.Stop()

	a.taskInFlight.Store(&taskContext{TaskID: "task-1", OriginalPrompt: "ask me"})

	resultRaw := []byte(`{
		"type": "result",
		"session_id": "claude-abc",
		"permission_denials": [
			{
				"tool_name": "AskUserQuestion",
				"tool_use_id": "toolu_002",
				"tool_input": {
					"questions": [
						{
							"question": "Favorite color?",
							"options": [
								{"label": "red", "description": "the color of fire"},
								{"label": "blue", "description": "the color of water"}
							]
						}
					]
				}
			}
		]
	}`)

	_ = a.handleResult(resultRaw)

	var questions []*protocol.Question
	for _, f := range relay.GetFrames() {
		if q, ok := f.(*protocol.Question); ok {
			questions = append(questions, q)
		}
	}
	if len(questions) != 1 {
		t.Fatalf("expected 1 Question, got %d", len(questions))
	}
	if questions[0].Question != "Favorite color?" {
		t.Errorf("question text: %s", questions[0].Question)
	}
	if len(questions[0].Options) != 2 || questions[0].Options[0] != "red" {
		t.Errorf("options: %+v", questions[0].Options)
	}
}

func TestActorRestartsWithAllowedToolsOnApproval(t *testing.T) {
	relay := &fakeRelay{}
	tmpDir := t.TempDir()

	// Use the fake-claude binary so that RestartWithGrant's new executor
	// actually starts and Send unblocks. Without a real binary the executor
	// would block forever on its `ready` channel and the test would only ever
	// observe state via races.
	binPath := buildFakeClaude(t)

	actor, err := NewActor(Options{
		SessionID:  "s-restart",
		ChannelID:  "c",
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		WALPath:    filepath.Join(tmpDir, "s-restart.jsonl"),
		Relay:      relay,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	defer actor.Stop()

	// Seed state: an in-flight task, a known claude session id, and a pending denial.
	actor.taskInFlight.Store(&taskContext{
		TaskID:         "task-1",
		OriginalPrompt: "Write hello.txt",
	})
	actor.claudeSessionID.Store("fake-session-123")
	actor.pendingDenial.Store(&pendingDenial{
		Denials: []string{"Write"},
		TaskID:  "task-1",
		Prompt:  "Write hello.txt",
	})

	// HandlePermissionResponse runs synchronously: it appends to allowedTools,
	// closes the (nil) old executor, builds + starts the new executor, and
	// re-sends the original prompt. Send blocks on the new executor's ready
	// channel which is closed by cmd.Start, so this returns once fake-claude
	// is spawned and the prompt is delivered.
	if err := actor.HandlePermissionResponse(&protocol.PermissionResponse{
		Type:      protocol.MsgTypePermissionResponse,
		SessionID: "s-restart",
		ChannelID: "c",
		RequestID: "toolu_001",
		Approved:  true,
	}); err != nil {
		t.Fatalf("HandlePermissionResponse: %v", err)
	}

	// allowedTools is now updated. Read via the lock-protected accessor.
	allowed := actor.AllowedTools()
	if len(allowed) != 1 || allowed[0] != "Write" {
		t.Errorf("allowedTools: %+v", allowed)
	}

	// pendingDenial is cleared before RestartWithGrant is even called.
	if pd, ok := actor.pendingDenial.Load().(*pendingDenial); ok && pd != nil {
		t.Error("pending denial should be cleared after approval")
	}
}

func TestActorRejectsSendTaskWhenPendingDenial(t *testing.T) {
	relay := &fakeRelay{}
	tmpDir := t.TempDir()
	a, _ := NewActor(Options{
		SessionID: "s-1",
		ChannelID: "c-1",
		WALPath:   filepath.Join(tmpDir, "s-1.jsonl"),
		Relay:     relay,
	})
	defer a.Stop()

	a.pendingDenial.Store(&pendingDenial{
		Denials: []string{"Write"},
		TaskID:  "task-1",
		Prompt:  "original",
	})

	err := a.SendTask(protocol.Task{
		TaskID:    "task-2",
		SessionID: "s-1",
		ChannelID: "c-1",
		Prompt:    "new task",
	})
	if err == nil {
		t.Fatal("expected error when pendingDenial is set, got nil")
	}
}
