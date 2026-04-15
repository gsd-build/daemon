package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClaimCronRunSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/daemon/cron-runs/claim" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("authorization"); got != "Bearer daemon-token" {
			t.Fatalf("unexpected auth header: %q", got)
		}

		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["machineId"] != "machine-1" || body["cronJobId"] != "cron-1" {
			t.Fatalf("unexpected request body: %+v", body)
		}

		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"cronRunId": "run-1",
			"task": map[string]any{
				"taskId":              "task-1",
				"sessionId":           "session-1",
				"channelId":           "cron-cron-1-123",
				"prompt":              "run it",
				"model":               "claude-opus-4-6[1m]",
				"effort":              "max",
				"permissionMode":      "acceptEdits",
				"cwd":                 "/tmp/project",
				"personaSystemPrompt": "Stay concise.",
				"claudeSessionId":     "claude-session-1",
			},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL)
	resp, err := client.ClaimCronRun(ClaimCronRunRequest{
		MachineID:      "machine-1",
		CronJobID:      "cron-1",
		ScheduledFor:   "2026-04-14T13:00:00Z",
		IdempotencyKey: "key-1",
		AuthToken:      "daemon-token",
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if resp.CronRunID != "run-1" || resp.Task.TaskID != "task-1" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestClaimCronRunHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"occurrence_already_finished"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	if _, err := client.ClaimCronRun(ClaimCronRunRequest{
		MachineID:      "machine-1",
		CronJobID:      "cron-1",
		ScheduledFor:   "2026-04-14T13:00:00Z",
		IdempotencyKey: "key-1",
		AuthToken:      "daemon-token",
	}); err == nil {
		t.Fatal("expected error")
	}
}

func TestClaimCronRunMalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"cronRunId":`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	if _, err := client.ClaimCronRun(ClaimCronRunRequest{
		MachineID:      "machine-1",
		CronJobID:      "cron-1",
		ScheduledFor:   "2026-04-14T13:00:00Z",
		IdempotencyKey: "key-1",
		AuthToken:      "daemon-token",
	}); err == nil {
		t.Fatal("expected error")
	}
}
