// Package e2e contains end-to-end integration tests for the daemon.
// These tests assemble the real daemon wired to an in-process stub relay
// and a fake Pi subprocess, then drive it through scripted scenarios.
package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/gsd-build/daemon/internal/loop"
	protocol "github.com/gsd-build/protocol-go"
)

func TestE2EHappyPath(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e integration test in short mode")
	}

	const (
		machineID = "test-machine-1"
		authToken = "test-token-1"
		sessionID = "test-session-1"
		taskID    = "task-1"
		channelID = "ch-1"
	)

	// 1. Stub relay.
	relay := NewStubRelay(t)

	// 2. Temp home — daemon writes WAL under $HOME/.gsd-cloud/wal.
	home := makeTestHome(t)
	t.Setenv("HOME", home)

	fakePi := writeFakePi(t, home)
	t.Setenv("GSD_PI_EXTENSION_PATH", writeFakePiExtension(t, home))

	cfg := makeTestConfig(relay.URL(), machineID, authToken)
	cwd := t.TempDir()

	daemon, err := loop.NewWithPiBinaryPath(cfg, "test-version", fakePi)
	if err != nil {
		t.Fatalf("loop.NewWithPiBinaryPath: %v", err)
	}

	// 7. Run the daemon in a goroutine.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- daemon.Run(ctx)
	}()

	// 8. Wait for daemon to dial the stub relay.
	if err := relay.WaitForConnection(5 * time.Second); err != nil {
		t.Fatalf("waiting for daemon connection: %v", err)
	}

	// 9. Daemon must send Hello first.
	helloEnv, err := relay.WaitForFrame(protocol.MsgTypeHello, 3*time.Second)
	if err != nil {
		t.Fatalf("waiting for Hello: %v", err)
	}
	hello, ok := helloEnv.Payload.(*protocol.Hello)
	if !ok {
		t.Fatalf("Hello payload type: got %T", helloEnv.Payload)
	}
	if hello.MachineID != machineID {
		t.Fatalf("Hello.MachineID: got %q want %q", hello.MachineID, machineID)
	}

	// 10. Send Welcome back so daemon's Connect() returns.
	if err := relay.Send(&protocol.Welcome{
		Type: protocol.MsgTypeWelcome,
	}); err != nil {
		t.Fatalf("send Welcome: %v", err)
	}

	// 11. Send a Task to the daemon.
	if err := relay.Send(&protocol.Task{
		Type:      protocol.MsgTypeTask,
		TaskID:    taskID,
		SessionID: sessionID,
		ChannelID: channelID,
		Prompt:    "hello",
		CWD:       cwd,
	}); err != nil {
		t.Fatalf("send Task: %v", err)
	}

	// 12. Daemon emits TaskStarted.
	if _, err := relay.WaitForFrame(protocol.MsgTypeTaskStarted, 5*time.Second); err != nil {
		t.Fatalf("waiting for TaskStarted: %v", err)
	}

	// 13. Daemon emits at least one Stream frame.
	if _, err := relay.WaitForFrame(protocol.MsgTypeStream, 5*time.Second); err != nil {
		t.Fatalf("waiting for Stream: %v", err)
	}

	// 14. Daemon emits TaskComplete with the expected metadata.
	completeEnv, err := relay.WaitForFrame(protocol.MsgTypeTaskComplete, 15*time.Second)
	if err != nil {
		t.Fatalf("waiting for TaskComplete: %v", err)
	}
	complete, ok := completeEnv.Payload.(*protocol.TaskComplete)
	if !ok {
		t.Fatalf("TaskComplete payload type: got %T", completeEnv.Payload)
	}
	if complete.ClaudeSessionID != "" {
		t.Fatalf("TaskComplete.ClaudeSessionID: got %q want empty", complete.ClaudeSessionID)
	}
	if complete.InputTokens <= 0 {
		t.Fatalf("TaskComplete.InputTokens: got %d, want > 0", complete.InputTokens)
	}
	if complete.OutputTokens <= 0 {
		t.Fatalf("TaskComplete.OutputTokens: got %d, want > 0", complete.OutputTokens)
	}

	// 15. No PermissionRequest frames in the happy path.
	for _, env := range relay.Received() {
		if env.Type == protocol.MsgTypePermissionRequest {
			t.Fatalf("unexpected PermissionRequest frame in happy path")
		}
	}

	// 16. Cancel daemon and wait for clean shutdown.
	cancel()
	select {
	case <-runErrCh:
		// Daemon returned (expected: ctx canceled / read error).
	case <-time.After(5 * time.Second):
		t.Fatalf("daemon did not shut down within 5s after cancel")
	}
}

func TestDaemonWarmPiWorkerReusesProcessAcrossTasks(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e integration test in short mode")
	}

	const (
		machineID = "test-machine-warm"
		authToken = "test-token-warm"
		sessionID = "test-session-warm"
		channelID = "ch-warm"
	)

	relay := NewStubRelay(t)
	home := makeTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("GSD_PI_EXTENSION_PATH", writeFakePiExtension(t, home))
	fakePi := writeWarmFakePi(t, home)

	cfg := makeTestConfig(relay.URL(), machineID, authToken)
	cwd := t.TempDir()

	daemon, err := loop.NewWithPiBinaryPath(cfg, "test-version", fakePi)
	if err != nil {
		t.Fatalf("loop.NewWithPiBinaryPath: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- daemon.Run(ctx)
	}()

	if err := relay.WaitForConnection(5 * time.Second); err != nil {
		t.Fatalf("waiting for daemon connection: %v", err)
	}
	if _, err := relay.WaitForFrame(protocol.MsgTypeHello, 3*time.Second); err != nil {
		t.Fatalf("waiting for Hello: %v", err)
	}
	if err := relay.Send(&protocol.Welcome{Type: protocol.MsgTypeWelcome}); err != nil {
		t.Fatalf("send Welcome: %v", err)
	}

	waitForTaskFrame := func(msgType string, taskID string, timeout time.Duration) {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			for _, env := range relay.Received() {
				if env.Type != msgType {
					continue
				}
				switch payload := env.Payload.(type) {
				case *protocol.TaskStarted:
					if payload.TaskID == taskID {
						return
					}
				case *protocol.TaskComplete:
					if payload.TaskID == taskID {
						return
					}
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("waiting for %s %s", msgType, taskID)
	}

	for _, taskID := range []string{"task-warm-1", "task-warm-2"} {
		if err := relay.Send(&protocol.Task{
			Type: protocol.MsgTypeTask, TaskID: taskID, SessionID: sessionID,
			ChannelID: channelID, Prompt: taskID, CWD: cwd, Engine: "pi",
		}); err != nil {
			t.Fatalf("send Task %s: %v", taskID, err)
		}
		waitForTaskFrame(protocol.MsgTypeTaskStarted, taskID, 5*time.Second)
		waitForTaskFrame(protocol.MsgTypeTaskComplete, taskID, 15*time.Second)
	}

	workers := daemon.Workers()
	if len(workers) != 1 {
		t.Fatalf("workers = %d, want 1", len(workers))
	}
	if workers[0].State != "idle" {
		t.Fatalf("worker state = %q, want idle", workers[0].State)
	}

	cancel()
	select {
	case <-runErrCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("daemon did not shut down within 5s after cancel")
	}
}

func TestE2EQuestionFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e integration test in short mode")
	}

	const (
		machineID = "test-machine-perm"
		authToken = "test-token-perm"
		sessionID = "test-session-perm"
		taskID    = "task-perm-1"
		channelID = "ch-perm"
	)

	relay := NewStubRelay(t)

	home := makeTestHome(t)
	t.Setenv("HOME", home)

	fakePi := writeFakePi(t, home)
	t.Setenv("GSD_PI_EXTENSION_PATH", writeFakePiExtension(t, home))
	t.Setenv("FAKE_PI_ASK_HUMAN", "1")

	cfg := makeTestConfig(relay.URL(), machineID, authToken)
	cwd := t.TempDir()

	daemon, err := loop.NewWithPiBinaryPath(cfg, "test-version", fakePi)
	if err != nil {
		t.Fatalf("loop.NewWithPiBinaryPath: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- daemon.Run(ctx)
	}()

	if err := relay.WaitForConnection(5 * time.Second); err != nil {
		t.Fatalf("waiting for daemon connection: %v", err)
	}

	if _, err := relay.WaitForFrame(protocol.MsgTypeHello, 3*time.Second); err != nil {
		t.Fatalf("waiting for Hello: %v", err)
	}

	if err := relay.Send(&protocol.Welcome{
		Type: protocol.MsgTypeWelcome,
	}); err != nil {
		t.Fatalf("send Welcome: %v", err)
	}

	if err := relay.Send(&protocol.Task{
		Type:      protocol.MsgTypeTask,
		TaskID:    taskID,
		SessionID: sessionID,
		ChannelID: channelID,
		Prompt:    "Write a file please",
		CWD:       cwd,
	}); err != nil {
		t.Fatalf("send Task: %v", err)
	}

	questionEnv, err := relay.WaitForFrame(protocol.MsgTypeQuestion, 10*time.Second)
	if err != nil {
		t.Fatalf("waiting for Question: %v", err)
	}
	question, ok := questionEnv.Payload.(*protocol.Question)
	if !ok {
		t.Fatalf("Question payload type: got %T", questionEnv.Payload)
	}
	if question.Question != "Which path should I take?" {
		t.Fatalf("Question.Question: got %q", question.Question)
	}

	if err := relay.Send(&protocol.QuestionResponse{
		Type:      protocol.MsgTypeQuestionResponse,
		ChannelID: channelID,
		SessionID: sessionID,
		RequestID: question.RequestID,
		Answer:    "Use Pi.",
	}); err != nil {
		t.Fatalf("send QuestionResponse: %v", err)
	}

	completeEnv, err := relay.WaitForFrame(protocol.MsgTypeTaskComplete, 15*time.Second)
	if err != nil {
		t.Fatalf("waiting for TaskComplete after answer: %v", err)
	}
	if _, ok := completeEnv.Payload.(*protocol.TaskComplete); !ok {
		t.Fatalf("TaskComplete payload type: got %T", completeEnv.Payload)
	}

	cancel()
	select {
	case <-runErrCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("daemon did not shut down within 5s after cancel")
	}
}
