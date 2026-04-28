package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gsd-build/daemon/internal/claude"
	"github.com/gsd-build/daemon/internal/pi"
	protocol "github.com/gsd-build/protocol-go"
)

// TestFileActivity_EndToEnd exercises the full daemon emit path:
// pi NDJSON → executor parsing → tool execution callbacks → Actor capture →
// protocol.Stream frame delivered to the relay. It pipes a synthetic pi
// stream containing a write tool_execution_start followed by a successful
// tool_execution_end, then asserts the relay observed a file_activity
// Stream frame with the expected op, path, and channel.
func TestFileActivity_EndToEnd(t *testing.T) {
	const (
		sessionID  = "sess-e2e"
		channelID  = "ch-e2e"
		toolCallID = "toolu_e2e_1"
		path       = "hello.txt"
		cwd        = "/work/repo"
	)

	relay := newFakeRelay()
	actor, err := NewActor(Options{
		SessionID: sessionID,
		CWD:       cwd,
		Relay:     relay,
		Model:     "claude-sonnet-4-6",
	})
	if err != nil {
		t.Fatalf("NewActor: %v", err)
	}
	defer actor.Stop()

	// Wire the executor exactly the way Actor.runWithPi does — but feed it a
	// synthetic NDJSON reader instead of a real pi subprocess.
	exec := pi.NewExecutor(pi.Options{CWD: cwd})
	coordinator := &structuredQuestionCoordinator{}
	exec.OnToolExecutionStart = actor.capturePiToolStart(coordinator)
	exec.OnToolExecutionEnd = actor.capturePiToolEnd(channelID)

	ndjson := strings.Join([]string{
		`{"type":"tool_execution_start","toolCallId":"` + toolCallID + `","toolName":"write","args":{"path":"` + path + `","content":"hi"}}`,
		`{"type":"tool_execution_end","toolCallId":"` + toolCallID + `","toolName":"write","result":{"content":[{"type":"text","text":"ok"}]},"isError":false}`,
		"",
	}, "\n")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := exec.StreamFromReaderForTest(
		ctx,
		strings.NewReader(ndjson),
		func(_ claude.Event) error { return nil },
		nil,
	); err != nil {
		t.Fatalf("StreamFromReaderForTest: %v", err)
	}

	frames := waitForFileActivity(t, relay, 2*time.Second)
	if len(frames) != 1 {
		t.Fatalf("expected exactly 1 file_activity frame, got %d", len(frames))
	}
	frame := frames[0]

	if frame.Type != protocol.MsgTypeStream {
		t.Errorf("frame.Type = %q, want %q", frame.Type, protocol.MsgTypeStream)
	}
	if frame.SessionID != sessionID {
		t.Errorf("frame.SessionID = %q, want %q", frame.SessionID, sessionID)
	}
	if frame.ChannelID != channelID {
		t.Errorf("frame.ChannelID = %q, want %q", frame.ChannelID, channelID)
	}

	var payload map[string]any
	if err := json.Unmarshal(frame.Event, &payload); err != nil {
		t.Fatalf("unmarshal file_activity event: %v", err)
	}
	if payload["type"] != "file_activity" {
		t.Errorf("event.type = %v, want file_activity", payload["type"])
	}
	if payload["op"] != "write" {
		t.Errorf("event.op = %v, want write", payload["op"])
	}
	if payload["path"] != path {
		t.Errorf("event.path = %v, want %q", payload["path"], path)
	}
	if payload["toolCallId"] != toolCallID {
		t.Errorf("event.toolCallId = %v, want %q", payload["toolCallId"], toolCallID)
	}
}

// waitForFileActivity polls the fake relay until at least one file_activity
// Stream frame is observed or timeout elapses. Avoids arbitrary sleeps.
func waitForFileActivity(t *testing.T, r *fakeRelay, timeout time.Duration) []*protocol.Stream {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if frames := collectFileActivityFrames(r.GetFrames()); len(frames) > 0 {
			return frames
		}
		time.Sleep(10 * time.Millisecond)
	}
	return collectFileActivityFrames(r.GetFrames())
}
