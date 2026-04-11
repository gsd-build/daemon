// Package e2e contains end-to-end integration tests for the daemon.
// These tests assemble the real daemon wired to an in-process stub relay
// and a fake-claude subprocess, then drive it through scripted scenarios.
package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gsd-build/daemon/internal/display"
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

	// 3. Build fake-claude.
	fakeClaude := buildFakeClaude(t, home)

	// 4. Test config pointed at the stub relay.
	cfg := makeTestConfig(relay.URL(), machineID, authToken)

	// 5. CWD for spawned fake-claude — must exist.
	cwd := t.TempDir()

	// 6. Build the daemon with fake-claude as the spawned binary.
	daemon, err := loop.NewWithBinaryPath(cfg, "test-version", fakeClaude, display.Default)
	if err != nil {
		t.Fatalf("loop.NewWithBinaryPath: %v", err)
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
	if complete.ClaudeSessionID != "fake-session-123" {
		t.Fatalf("TaskComplete.ClaudeSessionID: got %q want %q", complete.ClaudeSessionID, "fake-session-123")
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

func TestE2EPermissionFlow(t *testing.T) {
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

	fakeClaude := buildFakeClaude(t, home)

	// fake-claude reads these env vars; the daemon's executor inherits the
	// parent process environment when Options.Env is nil, so t.Setenv reaches
	// the spawned subprocess.
	argsFile := filepath.Join(home, "fake-claude-args.json")
	t.Setenv("FAKE_CLAUDE_DENY_TOOL", "Write")
	t.Setenv("FAKE_CLAUDE_ARGS_FILE", argsFile)

	cfg := makeTestConfig(relay.URL(), machineID, authToken)
	cwd := t.TempDir()

	daemon, err := loop.NewWithBinaryPath(cfg, "test-version", fakeClaude, display.Default)
	if err != nil {
		t.Fatalf("loop.NewWithBinaryPath: %v", err)
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

	// Daemon should forward the permission_denial as a PermissionRequest.
	permEnv, err := relay.WaitForFrame(protocol.MsgTypePermissionRequest, 10*time.Second)
	if err != nil {
		t.Fatalf("waiting for PermissionRequest: %v", err)
	}
	permReq, ok := permEnv.Payload.(*protocol.PermissionRequest)
	if !ok {
		t.Fatalf("PermissionRequest payload type: got %T", permEnv.Payload)
	}
	if permReq.ToolName != "Write" {
		t.Fatalf("PermissionRequest.ToolName: got %q want %q", permReq.ToolName, "Write")
	}

	// Approve the permission.
	if err := relay.Send(&protocol.PermissionResponse{
		Type:      protocol.MsgTypePermissionResponse,
		ChannelID: channelID,
		SessionID: sessionID,
		RequestID: permReq.RequestID,
		Approved:  true,
	}); err != nil {
		t.Fatalf("send PermissionResponse: %v", err)
	}

	// After approval, the daemon should re-spawn fake-claude with --allowedTools
	// and ultimately emit TaskComplete.
	completeEnv, err := relay.WaitForFrame(protocol.MsgTypeTaskComplete, 15*time.Second)
	if err != nil {
		t.Fatalf("waiting for TaskComplete after approval: %v", err)
	}
	if _, ok := completeEnv.Payload.(*protocol.TaskComplete); !ok {
		t.Fatalf("TaskComplete payload type: got %T", completeEnv.Payload)
	}

	// Verify the second fake-claude invocation included --allowedTools Write.
	// FAKE_CLAUDE_ARGS_FILE is overwritten on each invocation, so the file
	// reflects the most recent (post-approval) call.
	argsData, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read fake-claude args file: %v", err)
	}
	var args []string
	if err := json.Unmarshal(argsData, &args); err != nil {
		t.Fatalf("unmarshal fake-claude args: %v", err)
	}
	foundAllowed := false
	for i, a := range args {
		if a == "--allowedTools" && i+1 < len(args) && strings.Contains(args[i+1], "Write") {
			foundAllowed = true
			break
		}
	}
	if !foundAllowed {
		t.Fatalf("expected post-approval fake-claude invocation to include --allowedTools Write, got args: %v", args)
	}

	cancel()
	select {
	case <-runErrCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("daemon did not shut down within 5s after cancel")
	}
}
