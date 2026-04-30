package session

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gsd-build/daemon/internal/pi"
	protocol "github.com/gsd-build/protocol-go"
)

func TestPlanRuntimeReporterFlushesBoundedEvidencePosts(t *testing.T) {
	var mu sync.Mutex
	var payloads []planEvidencePayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent-plan/evidence" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer gsd_plan_test" {
			t.Fatalf("authorization = %q", got)
		}
		var payload planEvidencePayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		mu.Lock()
		payloads = append(payloads, payload)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	reporter := newPlanRuntimeReporter("11111111-1111-4111-8111-111111111111", &protocol.PlanCapability{
		APIBaseURL: server.URL,
		Token:      "gsd_plan_test",
		ExpiresAt:  time.Now().Add(time.Hour).Format(time.RFC3339),
	})
	reporter.now = fixedPlanEvidenceClock()
	for i := 0; i < planEvidenceMaxFlushPosts+10; i++ {
		toolCallID := "toolu_test_" + string(rune('a'+(i%26)))
		reporter.RecordToolStart(pi.ToolExecutionStart{
			ToolCallID: toolCallID,
			ToolName:   "bash",
			Args:       map[string]any{"command": "go test ./..."},
		})
		reporter.RecordToolEnd(pi.ToolExecutionEnd{
			ToolCallID: toolCallID,
			ToolName:   "bash",
		})
	}

	reporter.Flush(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(payloads) != planEvidenceMaxFlushPosts {
		t.Fatalf("posted evidence count = %d, want %d", len(payloads), planEvidenceMaxFlushPosts)
	}
	seenToolCall := false
	seenTest := false
	for _, payload := range payloads {
		switch payload.Kind {
		case "tool_call":
			seenToolCall = true
			if payload.Status != "passed" {
				t.Fatalf("tool_call status = %q, want passed", payload.Status)
			}
		case "test":
			seenTest = true
			if payload.Refs[0].Type != "command" || payload.Refs[0].Value != "go test ./..." {
				t.Fatalf("test refs = %#v", payload.Refs)
			}
		}
	}
	if !seenToolCall || !seenTest {
		t.Fatalf("posted kinds missing tool_call=%v test=%v", seenToolCall, seenTest)
	}
}

func TestPlanRuntimeReporterRecordsFileChanges(t *testing.T) {
	reporter := newPlanRuntimeReporter("22222222-2222-4222-8222-222222222222", &protocol.PlanCapability{
		APIBaseURL: "https://app.test",
		Token:      "gsd_plan_test",
		ExpiresAt:  time.Now().Add(time.Hour).Format(time.RFC3339),
	})
	reporter.now = fixedPlanEvidenceClock()

	reporter.RecordToolStart(pi.ToolExecutionStart{
		ToolCallID: "toolu_write",
		ToolName:   "write",
		Args:       map[string]any{"path": "internal/session/plan_evidence.go"},
	})
	reporter.RecordToolEnd(pi.ToolExecutionEnd{
		ToolCallID: "toolu_write",
		ToolName:   "write",
	})

	entries := reporter.snapshot(0)
	var fileEvidence *planEvidencePayload
	for i := range entries {
		if entries[i].Kind == "file_change" {
			fileEvidence = &entries[i]
			break
		}
	}
	if fileEvidence == nil {
		t.Fatalf("expected file_change evidence in %#v", entries)
	}
	if fileEvidence.Status != "passed" {
		t.Fatalf("file evidence status = %q", fileEvidence.Status)
	}
	if len(fileEvidence.Refs) == 0 || fileEvidence.Refs[0].Type != "file" || fileEvidence.Refs[0].Value != "internal/session/plan_evidence.go" {
		t.Fatalf("file evidence refs = %#v", fileEvidence.Refs)
	}
}

func TestActorPlanEvidencePostFailureDoesNotFailTask(t *testing.T) {
	var evidencePosts int
	var revokePosts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agent-plan/evidence":
			evidencePosts++
			http.Error(w, "temporary failure", http.StatusInternalServerError)
		case "/api/agent-plan/capability/revoke":
			revokePosts++
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	relay := newFakeRelay()
	actor, err := NewActor(testPiOptions(t, Options{
		SessionID:    "sess-plan-evidence-failure",
		Relay:        relay,
		PiBinaryPath: writePlanEvidenceFakePi(t),
	}))
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}

	err = actor.executeTask(context.Background(), protocol.Task{
		TaskID:    "33333333-3333-4333-8333-333333333333",
		SessionID: "sess-plan-evidence-failure",
		ChannelID: "ch-plan-evidence-failure",
		Prompt:    "remember this",
		Engine:    "pi",
		PlanCapability: &protocol.PlanCapability{
			APIBaseURL: server.URL,
			Token:      "gsd_plan_failure",
			ExpiresAt:  time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	})
	if err != nil {
		t.Fatalf("executeTask: %v", err)
	}
	if !fakeRelayHasTaskComplete(relay, "33333333-3333-4333-8333-333333333333") {
		t.Fatalf("expected task complete frame")
	}
	if evidencePosts == 0 {
		t.Fatalf("expected evidence posts")
	}
	if revokePosts != 1 {
		t.Fatalf("revoke posts = %d, want 1", revokePosts)
	}
}

func TestActorTerminalTaskRevokesPlanCapability(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if got := r.Header.Get("Authorization"); got != "Bearer gsd_plan_revoke" {
			t.Fatalf("authorization = %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	relay := newFakeRelay()
	actor, err := NewActor(testPiOptions(t, Options{
		SessionID: "sess-plan-revoke",
		Relay:     relay,
	}))
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}

	err = actor.executeTask(context.Background(), protocol.Task{
		TaskID:    "44444444-4444-4444-8444-444444444444",
		SessionID: "sess-plan-revoke",
		ChannelID: "ch-plan-revoke",
		Prompt:    "remember this",
		Engine:    "pi",
		PlanCapability: &protocol.PlanCapability{
			APIBaseURL: server.URL,
			Token:      "gsd_plan_revoke",
			ExpiresAt:  time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	})
	if err != nil {
		t.Fatalf("executeTask: %v", err)
	}
	if !fakeRelayHasTaskComplete(relay, "44444444-4444-4444-8444-444444444444") {
		t.Fatalf("expected task complete frame")
	}

	var revokeCount int
	for _, path := range paths {
		if path == "/api/agent-plan/capability/revoke" {
			revokeCount++
		}
	}
	if revokeCount != 1 {
		t.Fatalf("revoke count = %d, paths=%v", revokeCount, paths)
	}
}

func fixedPlanEvidenceClock() func() time.Time {
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return now }
}

func fakeRelayHasTaskComplete(relay *fakeRelay, taskID string) bool {
	for _, frame := range relay.GetFrames() {
		if complete, ok := frame.(*protocol.TaskComplete); ok && complete.TaskID == taskID {
			return true
		}
	}
	return false
}

func writePlanEvidenceFakePi(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "fake-pi")
	script := `#!/bin/sh
IFS= read -r prompt_frame || true
printf '%s\n' '{"type":"agent_start"}'
printf '%s\n' '{"type":"tool_execution_start","toolCallId":"toolu_cmd","toolName":"bash","args":{"command":"go test ./..."}}'
printf '%s\n' '{"type":"tool_execution_end","toolCallId":"toolu_cmd","toolName":"bash","result":{"content":[{"type":"text","text":"ok"}],"details":{}},"isError":false}'
printf '%s\n' '{"type":"agent_end","messages":[{"role":"user","content":[{"type":"text","text":"remember this"}]},{"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.001}}}]}'
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}
	return path
}
