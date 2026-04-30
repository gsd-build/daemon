package cmd

import (
	"encoding/json"
	"strings"
	"time"
)

type logFilter struct {
	TaskID    string
	SessionID string
	Since     time.Duration
	Level     string
}

type logEvent struct {
	Time          string `json:"time"`
	Level         string `json:"level"`
	Msg           string `json:"msg"`
	Event         string `json:"event"`
	Phase         string `json:"phase"`
	TaskID        string `json:"taskId"`
	SessionID     string `json:"sessionId"`
	RequestID     string `json:"requestId"`
	TraceID       string `json:"traceId"`
	AttemptID     string `json:"attemptId"`
	AttemptNumber int    `json:"attemptNumber"`
	TurnKind      string `json:"turnKind"`
	ElapsedMs     int64  `json:"elapsedMs"`
	Model         string `json:"model"`
	Provider      string `json:"provider"`
	PID           int    `json:"pid"`
	FailureCode   string `json:"failureCode"`
	Retryable     bool   `json:"retryable"`
	PromptPreview string `json:"promptPreview"`
	Cleanup       string `json:"cleanup"`
	raw           string
}

func filterLogLines(lines []string, filter logFilter) []logEvent {
	out := make([]logEvent, 0, len(lines))
	cutoff := time.Time{}
	if filter.Since > 0 {
		cutoff = time.Now().Add(-filter.Since)
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event logEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		event.raw = line
		if filter.TaskID != "" && event.TaskID != filter.TaskID {
			continue
		}
		if filter.SessionID != "" && event.SessionID != filter.SessionID {
			continue
		}
		if !cutoff.IsZero() {
			parsed, err := time.Parse(time.RFC3339Nano, event.Time)
			if err == nil && parsed.Before(cutoff) {
				continue
			}
		}
		if filter.Level != "" && !levelAtLeast(event.Level, filter.Level) {
			continue
		}
		out = append(out, event)
	}
	return out
}

func levelAtLeast(got string, want string) bool {
	order := map[string]int{"DEBUG": 0, "INFO": 1, "WARN": 2, "ERROR": 3}
	gotLevel, gotOK := order[strings.ToUpper(got)]
	wantLevel, wantOK := order[strings.ToUpper(want)]
	if !gotOK || !wantOK {
		return false
	}
	return gotLevel >= wantLevel
}
