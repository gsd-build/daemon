package logging

import (
	"regexp"
	"strings"
)

const MaxPromptPreviewChars = 40
const MaxPromptPreviewWords = 8

var secretPattern = regexp.MustCompile(`(?i)(sk-[a-z0-9_-]{8,}|xox[baprs]-[a-z0-9-]{8,}|gh[pousr]_[a-z0-9_]{8,}|bearer\s+[a-z0-9._-]{8,})`)

type TaskLifecycleLogInput struct {
	Phase         string
	Status        string
	TaskID        string
	SessionID     string
	ChannelID     string
	RequestID     string
	TraceID       string
	AttemptID     string
	AttemptNumber int
	TurnKind      string
	ElapsedMs     int64
	Model         string
	Provider      string
	PID           int
	FailureCode   string
	Retryable     bool
	PromptText    string
	Cleanup       string
}

type TaskLifecycleLogEvent struct {
	Event         string `json:"event"`
	Phase         string `json:"phase"`
	Status        string `json:"status,omitempty"`
	TaskID        string `json:"taskId,omitempty"`
	SessionID     string `json:"sessionId,omitempty"`
	ChannelID     string `json:"channelId,omitempty"`
	RequestID     string `json:"requestId,omitempty"`
	TraceID       string `json:"traceId,omitempty"`
	AttemptID     string `json:"attemptId,omitempty"`
	AttemptNumber int    `json:"attemptNumber,omitempty"`
	TurnKind      string `json:"turnKind,omitempty"`
	ElapsedMs     int64  `json:"elapsedMs,omitempty"`
	Model         string `json:"model,omitempty"`
	Provider      string `json:"provider,omitempty"`
	PID           int    `json:"pid,omitempty"`
	FailureCode   string `json:"failureCode,omitempty"`
	Retryable     bool   `json:"retryable,omitempty"`
	PromptPreview string `json:"promptPreview,omitempty"`
	Cleanup       string `json:"cleanup,omitempty"`
}

func PromptPreview(prompt string) string {
	collapsed := strings.Join(strings.Fields(prompt), " ")
	if collapsed == "" {
		return ""
	}
	collapsed = secretPattern.ReplaceAllString(collapsed, "[REDACTED_SECRET]")
	words := strings.Fields(collapsed)
	if len(words) > MaxPromptPreviewWords {
		words = words[:MaxPromptPreviewWords]
	}
	kept := make([]string, 0, len(words))
	for _, word := range words {
		candidate := strings.Join(append(append([]string{}, kept...), word), " ")
		if len(candidate) > MaxPromptPreviewChars {
			break
		}
		kept = append(kept, word)
	}
	return strings.Join(kept, " ")
}

func NewTaskLifecycleLog(input TaskLifecycleLogInput) TaskLifecycleLogEvent {
	return TaskLifecycleLogEvent{
		Event:         "task_lifecycle",
		Phase:         input.Phase,
		Status:        input.Status,
		TaskID:        input.TaskID,
		SessionID:     input.SessionID,
		ChannelID:     input.ChannelID,
		RequestID:     input.RequestID,
		TraceID:       input.TraceID,
		AttemptID:     input.AttemptID,
		AttemptNumber: input.AttemptNumber,
		TurnKind:      input.TurnKind,
		ElapsedMs:     input.ElapsedMs,
		Model:         input.Model,
		Provider:      input.Provider,
		PID:           input.PID,
		FailureCode:   input.FailureCode,
		Retryable:     input.Retryable,
		PromptPreview: PromptPreview(input.PromptText),
		Cleanup:       input.Cleanup,
	}
}
