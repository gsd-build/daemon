package logging

import (
	"log/slog"

	protocol "github.com/gsd-build/protocol-go"
)

// TaskAttrs returns slog attributes for consistent log correlation from a Task message.
func TaskAttrs(task *protocol.Task) []any {
	attrs := []any{
		slog.String("taskId", task.TaskID),
		slog.String("sessionId", task.SessionID),
		slog.String("channelId", task.ChannelID),
	}
	if task.RequestID != "" {
		attrs = append(attrs, slog.String("requestId", task.RequestID))
	}
	if task.Traceparent != "" {
		attrs = append(attrs, slog.String("traceId", protocol.TraceID(task.Traceparent)))
	}
	return attrs
}
