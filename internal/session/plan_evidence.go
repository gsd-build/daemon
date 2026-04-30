package session

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gsd-build/daemon/internal/pi"
	protocol "github.com/gsd-build/protocol-go"
)

const (
	planEvidenceMaxFlushPosts = 80
	planEvidencePostAttempts  = 2
	planEvidencePostTimeout   = 2 * time.Second
)

type planEvidenceRef struct {
	Type  string `json:"type"`
	Label string `json:"label"`
	Value string `json:"value"`
}

type planEvidencePayload struct {
	ID          string            `json:"id"`
	TaskID      string            `json:"taskId,omitempty"`
	Kind        string            `json:"kind"`
	Status      string            `json:"status"`
	Summary     string            `json:"summary"`
	Refs        []planEvidenceRef `json:"refs"`
	StartedAt   string            `json:"startedAt,omitempty"`
	CompletedAt string            `json:"completedAt,omitempty"`
}

type planToolStart struct {
	evidenceID string
	index      int
	toolName   string
	args       map[string]any
	startedAt  time.Time
}

type planRuntimeReporter struct {
	taskID string
	cap    *protocol.PlanCapability
	client *http.Client
	now    func() time.Time

	mu         sync.Mutex
	entries    []planEvidencePayload
	toolStarts map[string]planToolStart
}

func newPlanRuntimeReporter(taskID string, cap *protocol.PlanCapability) *planRuntimeReporter {
	if cap == nil || strings.TrimSpace(cap.APIBaseURL) == "" || strings.TrimSpace(cap.Token) == "" {
		return nil
	}
	return &planRuntimeReporter{
		taskID:     taskID,
		cap:        cap,
		client:     &http.Client{},
		now:        time.Now,
		toolStarts: make(map[string]planToolStart),
	}
}

func (r *planRuntimeReporter) RecordToolStart(event pi.ToolExecutionStart) {
	if r == nil {
		return
	}
	now := r.now().UTC()
	id, err := newPlanEvidenceUUID()
	if err != nil {
		slog.Warn("plan evidence id generation failed", "toolCallID", event.ToolCallID, "err", err)
		return
	}
	toolName := cleanEvidenceText(event.ToolName, "tool")
	toolCallID := cleanEvidenceText(event.ToolCallID, id)
	entry := planEvidencePayload{
		ID:        id,
		TaskID:    r.taskID,
		Kind:      "tool_call",
		Status:    "running",
		Summary:   truncateEvidenceText(fmt.Sprintf("Tool running: %s", toolName), 1000),
		StartedAt: now.Format(time.RFC3339Nano),
		Refs: []planEvidenceRef{
			{Type: "tool_call", Label: truncateEvidenceText(toolName, 200), Value: truncateEvidenceText(toolCallID, 2000)},
		},
	}

	r.mu.Lock()
	index := len(r.entries)
	r.entries = append(r.entries, entry)
	if event.ToolCallID != "" {
		r.toolStarts[event.ToolCallID] = planToolStart{
			evidenceID: id,
			index:      index,
			toolName:   event.ToolName,
			args:       cloneEvidenceArgs(event.Args),
			startedAt:  now,
		}
	}
	r.mu.Unlock()
}

func (r *planRuntimeReporter) RecordToolEnd(event pi.ToolExecutionEnd) {
	if r == nil {
		return
	}
	now := r.now().UTC()
	status := "passed"
	if event.IsError {
		status = "failed"
	}

	r.mu.Lock()
	started, ok := r.toolStarts[event.ToolCallID]
	if ok {
		delete(r.toolStarts, event.ToolCallID)
		if started.index >= 0 && started.index < len(r.entries) && r.entries[started.index].ID == started.evidenceID {
			toolName := firstNonEmpty(event.ToolName, started.toolName, "tool")
			r.entries[started.index].Status = status
			r.entries[started.index].Summary = truncateEvidenceText(
				fmt.Sprintf("Tool %s: %s", status, toolName),
				1000,
			)
			r.entries[started.index].CompletedAt = now.Format(time.RFC3339Nano)
		}
	} else {
		started = planToolStart{
			toolName:  event.ToolName,
			args:      map[string]any{},
			startedAt: now,
		}
		r.entries = append(r.entries, makeToolEndEvidence(r.taskID, event, status, now))
	}

	command := commandFromToolArgs(started.args)
	toolName := firstNonEmpty(event.ToolName, started.toolName)
	if command != "" && isShellTool(toolName) {
		r.entries = append(r.entries, makeCommandEvidence(r.taskID, event.ToolCallID, command, status, now))
	}
	if path := fileToolPath(started.args); path != "" {
		if normalized, ok := normalizePiFileToolName(toolName); ok && normalized != "read" {
			r.entries = append(r.entries, makeFileEvidence(r.taskID, event.ToolCallID, normalized, path, status, now))
		}
	}
	r.mu.Unlock()
}

func (r *planRuntimeReporter) Flush(ctx context.Context) {
	if r == nil {
		return
	}
	entries := r.flushCandidates(planEvidenceMaxFlushPosts)
	for _, entry := range entries {
		if err := r.postJSONWithRetry(ctx, "/api/agent-plan/evidence", entry); err != nil {
			slog.Warn("plan evidence post failed",
				"taskId", r.taskID,
				"evidenceId", entry.ID,
				"kind", entry.Kind,
				"err", err,
			)
			continue
		}
		r.removeEntry(entry.ID)
	}
}

func (r *planRuntimeReporter) RevokeCapability(ctx context.Context) {
	if r == nil {
		return
	}
	payload := map[string]string{"taskId": r.taskID}
	if err := r.postJSONWithRetry(ctx, "/api/agent-plan/capability/revoke", payload); err != nil {
		slog.Warn("plan capability revoke failed", "taskId", r.taskID, "err", err)
	}
}

func (r *planRuntimeReporter) Finish(ctx context.Context) {
	if r == nil {
		return
	}
	r.Flush(ctx)
	r.RevokeCapability(ctx)
}

func (r *planRuntimeReporter) snapshot(limit int) []planEvidencePayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.entries)
	if limit > 0 && n > limit {
		n = limit
	}
	out := make([]planEvidencePayload, n)
	copy(out, r.entries[:n])
	return out
}

func (r *planRuntimeReporter) flushCandidates(limit int) []planEvidencePayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]planEvidencePayload, 0, minPositive(limit, len(r.entries)))
	for _, entry := range r.entries {
		if entry.Status == "running" {
			continue
		}
		out = append(out, entry)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (r *planRuntimeReporter) removeEntry(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, entry := range r.entries {
		if entry.ID != id {
			continue
		}
		r.entries = append(r.entries[:i], r.entries[i+1:]...)
		for key, started := range r.toolStarts {
			switch {
			case started.index == i && started.evidenceID == id:
				delete(r.toolStarts, key)
			case started.index > i:
				started.index--
				r.toolStarts[key] = started
			}
		}
		return
	}
}

func minPositive(a int, b int) int {
	if a > 0 && a < b {
		return a
	}
	return b
}

func (r *planRuntimeReporter) postJSONWithRetry(ctx context.Context, path string, payload any) error {
	if r == nil {
		return nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < planEvidencePostAttempts; attempt++ {
		postCtx, cancel := context.WithTimeout(ctx, planEvidencePostTimeout)
		req, reqErr := http.NewRequestWithContext(
			postCtx,
			http.MethodPost,
			strings.TrimRight(r.cap.APIBaseURL, "/")+path,
			bytes.NewReader(body),
		)
		if reqErr != nil {
			cancel()
			return reqErr
		}
		req.Header.Set("Authorization", "Bearer "+r.cap.Token)
		req.Header.Set("Content-Type", "application/json")

		resp, doErr := r.client.Do(req)
		if doErr != nil {
			lastErr = doErr
			cancel()
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		closeErr := resp.Body.Close()
		cancel()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return closeErr
		}
		lastErr = fmt.Errorf("status %d", resp.StatusCode)
	}
	return lastErr
}

func makeToolEndEvidence(taskID string, event pi.ToolExecutionEnd, status string, now time.Time) planEvidencePayload {
	id, err := newPlanEvidenceUUID()
	if err != nil {
		id = fallbackEvidenceID(now)
	}
	toolName := cleanEvidenceText(event.ToolName, "tool")
	toolCallID := cleanEvidenceText(event.ToolCallID, id)
	return planEvidencePayload{
		ID:          id,
		TaskID:      taskID,
		Kind:        "tool_call",
		Status:      status,
		Summary:     truncateEvidenceText(fmt.Sprintf("Tool %s: %s", status, toolName), 1000),
		CompletedAt: now.Format(time.RFC3339Nano),
		Refs: []planEvidenceRef{
			{Type: "tool_call", Label: truncateEvidenceText(toolName, 200), Value: truncateEvidenceText(toolCallID, 2000)},
		},
	}
}

func makeCommandEvidence(taskID string, toolCallID string, command string, status string, now time.Time) planEvidencePayload {
	id, err := newPlanEvidenceUUID()
	if err != nil {
		id = fallbackEvidenceID(now)
	}
	kind := classifyPlanCommand(command)
	label := "Command"
	switch kind {
	case "test":
		label = "Test"
	case "build":
		label = "Build"
	}
	return planEvidencePayload{
		ID:          id,
		TaskID:      taskID,
		Kind:        kind,
		Status:      status,
		Summary:     truncateEvidenceText(fmt.Sprintf("%s %s: %s", label, status, command), 1000),
		CompletedAt: now.Format(time.RFC3339Nano),
		Refs: []planEvidenceRef{
			{Type: "command", Label: label, Value: truncateEvidenceText(command, 2000)},
			{Type: "tool_call", Label: "tool_call", Value: truncateEvidenceText(cleanEvidenceText(toolCallID, id), 2000)},
		},
	}
}

func makeFileEvidence(taskID string, toolCallID string, op string, path string, status string, now time.Time) planEvidencePayload {
	id, err := newPlanEvidenceUUID()
	if err != nil {
		id = fallbackEvidenceID(now)
	}
	return planEvidencePayload{
		ID:          id,
		TaskID:      taskID,
		Kind:        "file_change",
		Status:      status,
		Summary:     truncateEvidenceText(fmt.Sprintf("File %s %s: %s", op, status, path), 1000),
		CompletedAt: now.Format(time.RFC3339Nano),
		Refs: []planEvidenceRef{
			{Type: "file", Label: truncateEvidenceText(op, 200), Value: truncateEvidenceText(path, 2000)},
			{Type: "tool_call", Label: "tool_call", Value: truncateEvidenceText(cleanEvidenceText(toolCallID, id), 2000)},
		},
	}
}

func commandFromToolArgs(args map[string]any) string {
	if args == nil {
		return ""
	}
	for _, key := range []string{"command", "cmd"} {
		if value, ok := args[key].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func isShellTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "bash", "shell":
		return true
	default:
		return false
	}
}

func classifyPlanCommand(command string) string {
	lower := strings.TrimSpace(strings.ToLower(command))
	testPrefixes := []string{
		"go test",
		"npm test",
		"npm run test",
		"pnpm test",
		"pnpm run test",
		"pnpm vitest",
		"npx vitest",
		"yarn test",
		"yarn run test",
		"vitest",
	}
	for _, prefix := range testPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return "test"
		}
	}
	buildPrefixes := []string{
		"go build",
		"npm run build",
		"pnpm build",
		"pnpm run build",
		"yarn build",
		"yarn run build",
		"next build",
		"tsc",
	}
	for _, prefix := range buildPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return "build"
		}
	}
	return "command"
}

func cloneEvidenceArgs(args map[string]any) map[string]any {
	if len(args) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func cleanEvidenceText(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func truncateEvidenceText(value string, maxLen int) string {
	value = strings.TrimSpace(value)
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	if maxLen <= 3 {
		return value[:maxLen]
	}
	return strings.TrimSpace(value[:maxLen-3]) + "..."
}

func newPlanEvidenceUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4],
		b[4:6],
		b[6:8],
		b[8:10],
		b[10:16],
	), nil
}

func fallbackEvidenceID(now time.Time) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012x", now.UnixNano()&0xffffffffffff)
}
