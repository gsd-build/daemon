package crons

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/gsd-build/daemon/internal/api"
	protocol "github.com/gsd-build/protocol-go"
)

// ClaimClient is the subset of the daemon API client used for cron claims.
type ClaimClient interface {
	ClaimCronRun(req api.ClaimCronRunRequest) (*api.ClaimCronRunResponse, error)
}

// RunRecorder persists daemon-local runtime metadata for cron inventory.
type RunRecorder interface {
	RecordRun(jobID string, scheduledFor time.Time) error
}

// TaskDispatcher injects a claimed task into the normal daemon task path.
type TaskDispatcher func(task *protocol.Task) error

// Runtime coordinates cloud claim allocation and local execution.
type Runtime struct {
	machineID     string
	tokenProvider func() string
	client        ClaimClient
	recorder      RunRecorder
	dispatch      TaskDispatcher
	log           *slog.Logger
}

func NewRuntime(
	machineID string,
	tokenProvider func() string,
	client ClaimClient,
	recorder RunRecorder,
	dispatch TaskDispatcher,
	log *slog.Logger,
) *Runtime {
	if log == nil {
		log = slog.Default()
	}
	return &Runtime{
		machineID:     machineID,
		tokenProvider: tokenProvider,
		client:        client,
		recorder:      recorder,
		dispatch:      dispatch,
		log:           log,
	}
}

// HandleDue claims a due cron occurrence in the cloud and dispatches the task locally.
func (r *Runtime) HandleDue(spec protocol.CronSpec, scheduledFor time.Time) error {
	idempotencyKey := claimKey(r.machineID, spec.ID, scheduledFor.UTC())
	claimed, err := r.client.ClaimCronRun(api.ClaimCronRunRequest{
		MachineID:      r.machineID,
		CronJobID:      spec.ID,
		ScheduledFor:   scheduledFor.UTC().Format(time.RFC3339Nano),
		IdempotencyKey: idempotencyKey,
		AuthToken:      r.tokenProvider(),
	})
	if err != nil {
		r.log.Warn("cron claim failed", "cronJobId", spec.ID, "scheduledFor", scheduledFor.Format(time.RFC3339Nano), "err", err)
		return err
	}

	task := &protocol.Task{
		Type:            protocol.MsgTypeTask,
		TaskID:          claimed.Task.TaskID,
		SessionID:       claimed.Task.SessionID,
		ChannelID:       claimed.Task.ChannelID,
		Prompt:          claimed.Task.Prompt,
		Model:           claimed.Task.Model,
		Effort:          claimed.Task.Effort,
		PermissionMode:  claimed.Task.PermissionMode,
		CWD:             claimed.Task.CWD,
		ClaudeSessionID: claimed.Task.ClaudeSessionID,
	}

	if err := r.dispatch(task); err != nil {
		r.log.Warn("cron dispatch failed", "cronJobId", spec.ID, "taskId", task.TaskID, "err", err)
		return err
	}
	if err := r.recorder.RecordRun(spec.ID, scheduledFor.UTC()); err != nil {
		r.log.Warn("record cron run failed", "cronJobId", spec.ID, "scheduledFor", scheduledFor.Format(time.RFC3339Nano), "err", err)
	}
	return nil
}

func claimKey(machineID, cronJobID string, scheduledFor time.Time) string {
	sum := sha256.Sum256([]byte(machineID + ":" + cronJobID + ":" + scheduledFor.Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:])
}
