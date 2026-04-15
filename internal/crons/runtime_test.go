package crons

import (
	"errors"
	"testing"
	"time"

	"github.com/gsd-build/daemon/internal/api"
	protocol "github.com/gsd-build/protocol-go"
)

type fakeClaimClient struct {
	resp *api.ClaimCronRunResponse
	err  error
	req  api.ClaimCronRunRequest
}

func (f *fakeClaimClient) ClaimCronRun(req api.ClaimCronRunRequest) (*api.ClaimCronRunResponse, error) {
	f.req = req
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

type fakeRecorder struct {
	jobID        string
	scheduledFor time.Time
}

func (f *fakeRecorder) RecordRun(jobID string, scheduledFor time.Time) error {
	f.jobID = jobID
	f.scheduledFor = scheduledFor
	return nil
}

func TestRuntimeClaimsAndDispatches(t *testing.T) {
	client := &fakeClaimClient{
		resp: &api.ClaimCronRunResponse{
			CronRunID: "run-1",
			Task: api.ClaimCronTask{
				TaskID:              "task-1",
				SessionID:           "session-1",
				ChannelID:           "cron-cron-1-123",
				Prompt:              "run tests",
				Model:               "claude-opus-4-6[1m]",
				Effort:              "max",
				PermissionMode:      "acceptEdits",
				CWD:                 "/tmp/project",
				PersonaSystemPrompt: "Stay concise.",
				ClaudeSessionID:     "claude-session-1",
			},
		},
	}
	recorder := &fakeRecorder{}
	var dispatched *protocol.Task

	runtime := NewRuntime(
		"machine-1",
		func() string { return "auth-token" },
		client,
		recorder,
		func(task *protocol.Task) error {
			dispatched = task
			return nil
		},
		nil,
	)

	scheduledFor := time.Date(2026, 4, 14, 13, 0, 0, 0, time.UTC)
	if err := runtime.HandleDue(testCronSpec("cron-1", "* * * * *"), scheduledFor); err != nil {
		t.Fatalf("HandleDue: %v", err)
	}

	if client.req.AuthToken != "auth-token" || client.req.CronJobID != "cron-1" {
		t.Fatalf("unexpected claim request: %+v", client.req)
	}
	if dispatched == nil || dispatched.TaskID != "task-1" {
		t.Fatalf("expected claimed task to be dispatched, got %+v", dispatched)
	}
	if recorder.jobID != "cron-1" || !recorder.scheduledFor.Equal(scheduledFor) {
		t.Fatalf("unexpected recorder values: %+v", recorder)
	}
}

func TestRuntimeDoesNotDispatchOnClaimFailure(t *testing.T) {
	client := &fakeClaimClient{err: errors.New("boom")}
	recorder := &fakeRecorder{}
	dispatched := false

	runtime := NewRuntime(
		"machine-1",
		func() string { return "auth-token" },
		client,
		recorder,
		func(task *protocol.Task) error {
			dispatched = true
			return nil
		},
		nil,
	)

	if err := runtime.HandleDue(testCronSpec("cron-1", "* * * * *"), time.Now().UTC()); err == nil {
		t.Fatal("expected error")
	}
	if dispatched {
		t.Fatal("did not expect dispatch on claim failure")
	}
}
