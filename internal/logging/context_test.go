package logging

import (
	"log/slog"
	"testing"

	protocol "github.com/gsd-build/protocol-go"
)

func TestTaskAttrsIncludesCorrelationFields(t *testing.T) {
	attrs := TaskAttrs(&protocol.Task{
		TaskID:      "task-1",
		SessionID:   "session-1",
		ChannelID:   "channel-1",
		RequestID:   "request-1",
		Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00",
	})

	got := map[string]string{}
	for _, attr := range attrs {
		slogAttr, ok := attr.(slog.Attr)
		if !ok {
			continue
		}
		got[slogAttr.Key] = slogAttr.Value.String()
	}

	expected := map[string]string{
		"taskId":    "task-1",
		"sessionId": "session-1",
		"channelId": "channel-1",
		"requestId": "request-1",
		"traceId":   "4bf92f3577b34da6a3ce929d0e0e4736",
	}
	for key, want := range expected {
		if got[key] != want {
			t.Fatalf("expected %s=%q, got %q", key, want, got[key])
		}
	}

	for _, removed := range []string{"taskOrigin", "scheduledTaskId", "scheduledRunId"} {
		if _, exists := got[removed]; exists {
			t.Fatalf("did not expect %s in attrs, got %q", removed, got[removed])
		}
	}
}
