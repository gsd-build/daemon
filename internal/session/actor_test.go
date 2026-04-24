package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	protocol "github.com/gsd-build/protocol-go"
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

func readArgsFile(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read args file: %v", err)
	}
	var argv []string
	if err := json.Unmarshal(data, &argv); err != nil {
		t.Fatalf("unmarshal args file: %v", err)
	}
	return argv
}

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

func (r *fakeRelay) Send(ctx context.Context, msg any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, msg)
	r.cond.Broadcast()
	return nil
}

func (r *fakeRelay) GetFrames() []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]any, len(r.frames))
	copy(out, r.frames)
	return out
}

func (r *fakeRelay) waitFor(t *testing.T, timeout time.Duration, predicate func([]any) bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)

	r.mu.Lock()
	defer r.mu.Unlock()

	if predicate(r.frames) {
		return true
	}

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

type blockingUploader struct {
	started chan context.Context
	done    chan struct{}
}

func newBlockingUploader() *blockingUploader {
	return &blockingUploader{
		started: make(chan context.Context, 1),
		done:    make(chan struct{}),
	}
}

func (u *blockingUploader) Upload(ctx context.Context, filename string, data []byte) (string, error) {
	select {
	case u.started <- ctx:
	default:
	}
	<-ctx.Done()
	close(u.done)
	return "", ctx.Err()
}

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

func (r *fakeRelay) waitForType(t *testing.T, msgType string, timeout time.Duration) bool {
	return r.waitFor(t, timeout, func(frames []any) bool {
		for _, f := range frames {
			switch v := f.(type) {
			case *protocol.TaskStarted:
				if v.Type == msgType {
					return true
				}
			case *protocol.TaskCancelled:
				if v.Type == msgType {
					return true
				}
			case *protocol.TaskComplete:
				if v.Type == msgType {
					return true
				}
			case *protocol.TaskError:
				if v.Type == msgType {
					return true
				}
			}
		}
		return false
	})
}

func TestCancelTask_ActorStaysAlive(t *testing.T) {
	binPath := buildFakeClaude(t)
	relay := newFakeRelay()

	actor, err := NewActor(Options{
		SessionID:  "sess-cancel",
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Relay:      relay,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	defer actor.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- actor.Run(ctx) }()

	// Send first task
	if err := actor.SendTask(protocol.Task{
		TaskID:    "t1",
		SessionID: "sess-cancel",
		ChannelID: "ch1",
		Prompt:    "hello",
	}); err != nil {
		t.Fatal(err)
	}

	// Wait for task to start (taskStarted frame)
	if !relay.waitForType(t, "taskStarted", 5*time.Second) {
		t.Fatal("timed out waiting for taskStarted")
	}

	// Cancel the task
	actor.CancelTask()

	// Wait for taskCancelled frame
	if !relay.waitForType(t, "taskCancelled", 5*time.Second) {
		t.Fatal("timed out waiting for taskCancelled")
	}

	// Verify the actor is still alive by sending a second task
	if err := actor.SendTask(protocol.Task{
		TaskID:    "t2",
		SessionID: "sess-cancel",
		ChannelID: "ch2",
		Prompt:    "world",
	}); err != nil {
		t.Fatalf("second task should succeed: %v", err)
	}

	if !relay.waitForTaskComplete(t, 10*time.Second) {
		t.Fatal("timed out waiting for second task to complete")
	}

	// Verify actor.Run() hasn't returned (actor is still alive)
	select {
	case err := <-done:
		t.Fatalf("actor.Run() should not have returned, got: %v", err)
	default:
		// good — still running
	}
}

func TestActorHappyPath(t *testing.T) {
	binPath := buildFakeClaude(t)
	relay := newFakeRelay()

	actor, err := NewActor(Options{
		SessionID:  "sess-1",
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Relay:      relay,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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

	if !relay.waitForTaskComplete(t, 10*time.Second) {
		t.Fatal("timed out waiting for TaskComplete frame")
	}
	_ = actor.Stop()

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

	var completes []*protocol.TaskComplete
	for _, f := range frames {
		if tc, ok := f.(*protocol.TaskComplete); ok {
			completes = append(completes, tc)
		}
	}
	if len(completes) != 1 {
		t.Fatalf("expected 1 taskComplete, got %d", len(completes))
	}
	if completes[0].ClaudeSessionID != "fake-session-123" {
		t.Errorf("expected claudeSessionId=fake-session-123, got %s", completes[0].ClaudeSessionID)
	}
}

func TestActorPropagatesRequestIDToLifecycleFrames(t *testing.T) {
	binPath := buildFakeClaude(t)
	relay := newFakeRelay()

	actor, err := NewActor(Options{
		SessionID:  "sess-request-id",
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Relay:      relay,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	defer actor.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = actor.Run(ctx) }()

	if err := actor.SendTask(protocol.Task{
		TaskID:      "task-request-id",
		SessionID:   "sess-request-id",
		ChannelID:   "ch-request-id",
		Prompt:      "hello",
		RequestID:   "11111111-2222-4333-8444-555555555555",
		Traceparent: "00-11111111222243338444555555555555-aaaaaaaaaaaaaaaa-01",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	if !relay.waitForTaskComplete(t, 10*time.Second) {
		t.Fatal("timed out waiting for TaskComplete frame")
	}

	var started *protocol.TaskStarted
	var completed *protocol.TaskComplete
	for _, frame := range relay.GetFrames() {
		switch v := frame.(type) {
		case *protocol.TaskStarted:
			started = v
		case *protocol.TaskComplete:
			completed = v
		}
	}
	if started == nil || completed == nil {
		t.Fatal("expected both TaskStarted and TaskComplete frames")
	}
	if started.RequestID != "11111111-2222-4333-8444-555555555555" {
		t.Fatalf("taskStarted requestId = %q", started.RequestID)
	}
	if completed.RequestID != "11111111-2222-4333-8444-555555555555" {
		t.Fatalf("taskComplete requestId = %q", completed.RequestID)
	}
}

func TestActorMalformedFinalResultEmitsTaskError(t *testing.T) {
	binPath := buildFakeClaude(t)
	relay := newFakeRelay()

	t.Setenv("FAKE_CLAUDE_INVALID_RESULT", "1")

	actor, err := NewActor(Options{
		SessionID:  "sess-invalid-result",
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Relay:      relay,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	defer actor.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = actor.Run(ctx) }()

	if err := actor.SendTask(protocol.Task{
		TaskID:    "task-invalid-result",
		SessionID: "sess-invalid-result",
		ChannelID: "ch-invalid-result",
		Prompt:    "hello",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	gotError := relay.waitFor(t, 10*time.Second, func(frames []any) bool {
		for _, f := range frames {
			if te, ok := f.(*protocol.TaskError); ok {
				return te.TaskID == "task-invalid-result"
			}
		}
		return false
	})
	if !gotError {
		t.Fatal("timed out waiting for TaskError frame")
	}

	for _, frame := range relay.GetFrames() {
		if tc, ok := frame.(*protocol.TaskComplete); ok && tc.TaskID == "task-invalid-result" {
			t.Fatal("expected malformed final result to avoid TaskComplete")
		}
	}
}

func TestActorPermissionDenialAndApproval(t *testing.T) {
	binPath := buildFakeClaude(t)
	relay := newFakeRelay()

	t.Setenv("FAKE_CLAUDE_DENY_TOOL", "Write")

	actor, err := NewActor(Options{
		SessionID:  "sess-perm",
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Relay:      relay,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	go func() { _ = actor.Run(ctx) }()

	if err := actor.SendTask(protocol.Task{
		TaskID:    "task-1",
		SessionID: "sess-perm",
		ChannelID: "ch-1",
		Prompt:    "Write a file",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	gotPerm := relay.waitFor(t, 10*time.Second, func(frames []any) bool {
		for _, f := range frames {
			if _, ok := f.(*protocol.PermissionRequest); ok {
				return true
			}
		}
		return false
	})
	if !gotPerm {
		t.Fatal("timed out waiting for PermissionRequest")
	}

	if err := actor.HandlePermissionResponse(&protocol.PermissionResponse{
		Type:      protocol.MsgTypePermissionResponse,
		SessionID: "sess-perm",
		ChannelID: "ch-1",
		RequestID: "toolu_fake_001",
		Approved:  true,
	}); err != nil {
		t.Fatalf("HandlePermissionResponse: %v", err)
	}

	if !relay.waitForTaskComplete(t, 10*time.Second) {
		t.Fatal("timed out waiting for TaskComplete after approval")
	}
	_ = actor.Stop()

	allowed := actor.AllowedTools()
	if len(allowed) != 1 || allowed[0] != "Write" {
		t.Errorf("allowedTools: %+v", allowed)
	}
}

func TestActorPermissionRequestTimesOut(t *testing.T) {
	binPath := buildFakeClaude(t)
	relay := newFakeRelay()

	t.Setenv("FAKE_CLAUDE_DENY_TOOL", "Write")

	actor, err := NewActor(Options{
		SessionID:  "sess-perm-timeout",
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Relay:      relay,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	actor.interactionTimeout = 50 * time.Millisecond
	defer actor.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = actor.Run(ctx) }()

	if err := actor.SendTask(protocol.Task{
		TaskID:    "task-perm-timeout",
		SessionID: "sess-perm-timeout",
		ChannelID: "ch-perm-timeout",
		Prompt:    "Write a file",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	gotTimeout := relay.waitFor(t, 2*time.Second, func(frames []any) bool {
		for _, f := range frames {
			if te, ok := f.(*protocol.TaskError); ok {
				return strings.Contains(te.Error, "permission response")
			}
		}
		return false
	})
	if !gotTimeout {
		t.Fatal("expected permission timeout TaskError")
	}
}

func TestActorBatchQuestions(t *testing.T) {
	binPath := buildFakeClaude(t)
	relay := newFakeRelay()

	// Emit 3 AskUserQuestion denials in a single result.
	t.Setenv("FAKE_CLAUDE_QUESTIONS", "3")

	actor, err := NewActor(Options{
		SessionID:  "sess-batch-q",
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Relay:      relay,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	go func() { _ = actor.Run(ctx) }()

	if err := actor.SendTask(protocol.Task{
		TaskID:    "task-1",
		SessionID: "sess-batch-q",
		ChannelID: "ch-1",
		Prompt:    "ask me things",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Wait for all 3 questions to arrive at the relay.
	gotQuestions := relay.waitFor(t, 10*time.Second, func(frames []any) bool {
		count := 0
		for _, f := range frames {
			if _, ok := f.(*protocol.Question); ok {
				count++
			}
		}
		return count == 3
	})
	if !gotQuestions {
		frames := relay.GetFrames()
		count := 0
		for _, f := range frames {
			if _, ok := f.(*protocol.Question); ok {
				count++
			}
		}
		t.Fatalf("expected 3 questions, got %d", count)
	}

	// Collect the requestIDs.
	var questionMsgs []*protocol.Question
	for _, f := range relay.GetFrames() {
		if q, ok := f.(*protocol.Question); ok {
			questionMsgs = append(questionMsgs, q)
		}
	}

	// Answer in reverse order to prove order independence.
	for i := len(questionMsgs) - 1; i >= 0; i-- {
		q := questionMsgs[i]
		if err := actor.HandleQuestionResponse(&protocol.QuestionResponse{
			Type:      protocol.MsgTypeQuestionResponse,
			SessionID: "sess-batch-q",
			ChannelID: "ch-1",
			RequestID: q.RequestID,
			Answer:    "answer-" + q.RequestID,
		}); err != nil {
			t.Fatalf("HandleQuestionResponse %s: %v", q.RequestID, err)
		}
	}

	// The actor should re-spawn and complete.
	if !relay.waitForTaskComplete(t, 10*time.Second) {
		t.Fatal("timed out waiting for TaskComplete after batch answers")
	}
	_ = actor.Stop()

	// Verify exactly 3 questions were sent as protocol.Question messages.
	if len(questionMsgs) != 3 {
		t.Errorf("expected 3 Question messages, got %d", len(questionMsgs))
	}
	for i, q := range questionMsgs {
		expected := fmt.Sprintf("Question %d?", i+1)
		if q.Question != expected {
			t.Errorf("question %d: got %q, want %q", i, q.Question, expected)
		}
		expectedHeader := fmt.Sprintf("Header %d", i+1)
		if q.Header != expectedHeader {
			t.Errorf("question %d: header = %q, want %q", i, q.Header, expectedHeader)
		}
		if !q.MultiSelect {
			t.Errorf("question %d: expected multiSelect true", i)
		}
		if len(q.Options) != 2 {
			t.Errorf("question %d: expected 2 options, got %d", i, len(q.Options))
		}
		if q.Options[0].Description == "" {
			t.Errorf("question %d: missing option description", i)
		}
		if q.Options[0].Preview == "" {
			t.Errorf("question %d: missing option preview", i)
		}
	}
}

func TestActorBatchQuestionsTimeout(t *testing.T) {
	binPath := buildFakeClaude(t)
	relay := newFakeRelay()

	t.Setenv("FAKE_CLAUDE_QUESTIONS", "2")

	actor, err := NewActor(Options{
		SessionID:  "sess-batch-q-timeout",
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Relay:      relay,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	actor.interactionTimeout = 50 * time.Millisecond
	defer actor.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = actor.Run(ctx) }()

	if err := actor.SendTask(protocol.Task{
		TaskID:    "task-batch-q-timeout",
		SessionID: "sess-batch-q-timeout",
		ChannelID: "ch-batch-q-timeout",
		Prompt:    "ask me things",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	gotTimeout := relay.waitFor(t, 2*time.Second, func(frames []any) bool {
		for _, f := range frames {
			if te, ok := f.(*protocol.TaskError); ok {
				return strings.Contains(te.Error, "question response")
			}
		}
		return false
	})
	if !gotTimeout {
		t.Fatal("expected question timeout TaskError")
	}
}

func TestMaybeUploadImagesUsesTaskContext(t *testing.T) {
	tmpDir := t.TempDir()
	imagePath := filepath.Join(tmpDir, "sample.png")
	if err := os.WriteFile(imagePath, []byte("png"), 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}

	uploader := newBlockingUploader()
	actor, err := NewActor(Options{
		SessionID: "sess-image-context",
		CWD:       tmpDir,
		Relay:     newFakeRelay(),
		Uploader:  uploader,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}

	raw, err := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []map[string]any{
				{
					"type": "tool_use",
					"name": "Read",
					"id":   "toolu_img_1",
					"input": map[string]any{
						"file_path": imagePath,
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal raw event: %v", err)
	}

	taskCtx, cancel := context.WithCancel(context.Background())
	actor.maybeUploadImages(taskCtx, raw, "ch-image-context", 1)

	select {
	case <-uploader.started:
	case <-time.After(2 * time.Second):
		t.Fatal("expected upload to start")
	}

	cancel()

	select {
	case <-uploader.done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected upload goroutine to stop after task context cancellation")
	}
}

func TestActorSendTaskWhenBusy(t *testing.T) {
	relay := newFakeRelay()
	a, err := NewActor(Options{
		SessionID: "s-1",
		Relay:     relay,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	defer a.Stop()

	_ = a.SendTask(protocol.Task{TaskID: "t1", Prompt: "first"})

	err = a.SendTask(protocol.Task{TaskID: "t2", Prompt: "second"})
	if err == nil {
		t.Fatal("expected error when task channel full")
	}
}

func TestActorTaskTimeout(t *testing.T) {
	binPath := buildFakeClaude(t)
	relay := newFakeRelay()

	// FAKE_CLAUDE_SLEEP makes fake-claude sleep for N seconds before producing output
	t.Setenv("FAKE_CLAUDE_SLEEP", "10")

	actor, err := NewActor(Options{
		SessionID:  "sess-timeout",
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Relay:      relay,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	defer actor.Stop()

	// Set a very short timeout for testing
	actor.taskTimeout = 1 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- actor.Run(ctx) }()

	if err := actor.SendTask(protocol.Task{
		TaskID:    "t-timeout",
		SessionID: "sess-timeout",
		ChannelID: "ch1",
		Prompt:    "slow task",
	}); err != nil {
		t.Fatal(err)
	}

	// Should get a TaskError with timeout message
	gotError := relay.waitFor(t, 10*time.Second, func(frames []any) bool {
		for _, f := range frames {
			if te, ok := f.(*protocol.TaskError); ok {
				if te.TaskID == "t-timeout" {
					return true
				}
			}
		}
		return false
	})
	if !gotError {
		t.Fatal("expected TaskError for timeout")
	}

	// Verify the actor is still alive
	select {
	case err := <-done:
		t.Fatalf("actor.Run() should not have returned, got: %v", err)
	default:
		// good
	}
}

func TestActorLastActiveAt(t *testing.T) {
	binPath := buildFakeClaude(t)
	relay := newFakeRelay()

	actor, err := NewActor(Options{
		SessionID:  "sess-active",
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Relay:      relay,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer actor.Stop()

	// LastActiveAt should be set to creation time
	initial := actor.LastActiveAt()
	if initial.IsZero() {
		t.Error("expected non-zero initial lastActiveAt")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = actor.Run(ctx) }()

	if err := actor.SendTask(protocol.Task{
		TaskID:    "t1",
		SessionID: "sess-active",
		ChannelID: "ch1",
		Prompt:    "hello",
	}); err != nil {
		t.Fatal(err)
	}

	if !relay.waitForTaskComplete(t, 10*time.Second) {
		t.Fatal("timed out waiting for TaskComplete")
	}

	updated := actor.LastActiveAt()
	if !updated.After(initial) {
		t.Errorf("expected lastActiveAt to advance: initial=%v updated=%v", initial, updated)
	}
}

func TestActorWritesPIDFile(t *testing.T) {
	binPath := buildFakeClaude(t)
	relay := newFakeRelay()
	pidDir := t.TempDir()

	actor, err := NewActor(Options{
		SessionID:  "sess-pid",
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Relay:      relay,
	})
	if err != nil {
		t.Fatal(err)
	}
	actor.pidDir = pidDir
	defer actor.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = actor.Run(ctx) }()

	if err := actor.SendTask(protocol.Task{
		TaskID:    "t1",
		SessionID: "sess-pid",
		ChannelID: "ch1",
		Prompt:    "hello",
	}); err != nil {
		t.Fatal(err)
	}

	if !relay.waitForTaskComplete(t, 10*time.Second) {
		t.Fatal("timed out waiting for TaskComplete")
	}

	// After completion, PID file should be cleaned up
	entries, err := os.ReadDir(pidDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected PID file to be cleaned up, found %d files", len(entries))
	}
}

func TestActorInfoExecutingState(t *testing.T) {
	relay := newFakeRelay()
	actor, err := NewActor(Options{
		SessionID:  "sess-info-exec",
		BinaryPath: "echo",
		CWD:        "/tmp",
		Relay:      relay,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate an in-flight task by setting fields directly.
	actor.taskMu.Lock()
	actor.taskID = "task-42"
	now := time.Now()
	actor.taskStartedAt = &now
	actor.idleSince = nil
	actor.taskMu.Unlock()

	info := actor.Info()
	if info.State != "executing" {
		t.Errorf("expected executing, got %s", info.State)
	}
	if info.TaskID != "task-42" {
		t.Errorf("expected task-42, got %s", info.TaskID)
	}
	if info.StartedAt == nil {
		t.Error("expected StartedAt to be set")
	}
	if info.IdleSince != nil {
		t.Error("expected IdleSince to be nil")
	}
}

func TestActorInfoIdleState(t *testing.T) {
	relay := newFakeRelay()
	actor, err := NewActor(Options{
		SessionID:  "sess-info-idle",
		BinaryPath: "echo",
		CWD:        "/tmp",
		Relay:      relay,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Actor starts idle — set idleSince.
	actor.taskMu.Lock()
	idleTime := time.Now().Add(-10 * time.Minute)
	actor.idleSince = &idleTime
	actor.taskMu.Unlock()

	info := actor.Info()
	if info.State != "idle" {
		t.Errorf("expected idle, got %s", info.State)
	}
	if info.TaskID != "" {
		t.Errorf("expected empty taskID, got %s", info.TaskID)
	}
	if info.IdleSince == nil {
		t.Error("expected IdleSince to be set")
	}
}
