package logging

import (
	"log/slog"
	"testing"

	protocol "github.com/gsd-build/protocol-go"
)

func TestTaskAttrsIncludesScheduledOrigin(t *testing.T) {
	attrs := TaskAttrs(&protocol.Task{
		TaskID:    "task-1",
		SessionID: "session-1",
		ChannelID: "channel-1",
		Origin: &protocol.TaskOrigin{
			Kind:            "scheduled",
			ScheduledTaskID: "scheduled-task-1",
			RunID:           "run-1",
		},
	})

	got := map[string]string{}
	for _, attr := range attrs {
		slogAttr, ok := attr.(slog.Attr)
		if !ok {
			continue
		}
		got[slogAttr.Key] = slogAttr.Value.String()
	}

	if got["taskOrigin"] != "scheduled" {
		t.Fatalf("expected taskOrigin=scheduled, got %q", got["taskOrigin"])
	}
	if got["scheduledTaskId"] != "scheduled-task-1" {
		t.Fatalf("expected scheduledTaskId, got %q", got["scheduledTaskId"])
	}
	if got["scheduledRunId"] != "run-1" {
		t.Fatalf("expected scheduledRunId, got %q", got["scheduledRunId"])
	}
}
