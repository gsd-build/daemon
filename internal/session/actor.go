// Package session ties the Claude executor, WAL, and relay together
// into one "session actor" per user session.
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gsd-cloud/daemon/internal/claude"
	"github.com/gsd-cloud/daemon/internal/wal"
	protocol "github.com/gsd-cloud/protocol-go"
)

// RelaySender is the minimal interface the actor needs to push events to the relay.
type RelaySender interface {
	Send(msg any) error
}

// Options configures a new Actor.
type Options struct {
	SessionID      string
	ChannelID      string
	BinaryPath     string
	CWD            string
	WALPath        string
	Relay          RelaySender
	Model          string
	Effort         string
	PermissionMode string
	SystemPrompt   string
	ResumeSession  string
	StartSeq       int64
}

// Actor drives a single Claude session.
type Actor struct {
	opts     Options
	log      *wal.Log
	executor *claude.Executor

	seq          atomic.Int64
	taskInFlight atomic.Value // *taskContext

	stopOnce sync.Once
	stopCh   chan struct{}

	claudeSessionID atomic.Value // string
}

type taskContext struct {
	TaskID    string
	StartedAt time.Time
	Input     int64
	Output    int64
	CostUSD   string
}

// NewActor creates a new Actor with a WAL rooted at opts.WALPath and
// initial sequence at opts.StartSeq.
func NewActor(opts Options) (*Actor, error) {
	log, err := wal.Open(opts.WALPath)
	if err != nil {
		return nil, fmt.Errorf("wal open: %w", err)
	}

	executor := claude.NewExecutor(claude.Options{
		BinaryPath:     opts.BinaryPath,
		CWD:            opts.CWD,
		Model:          opts.Model,
		Effort:         opts.Effort,
		PermissionMode: opts.PermissionMode,
		SystemPrompt:   opts.SystemPrompt,
		ResumeSession:  opts.ResumeSession,
	})

	a := &Actor{
		opts:     opts,
		log:      log,
		executor: executor,
		stopCh:   make(chan struct{}),
	}
	a.seq.Store(opts.StartSeq)
	return a, nil
}

// LastSequence returns the highest sequence number emitted so far.
func (a *Actor) LastSequence() int64 {
	return a.seq.Load()
}

// SendTask writes a user message into Claude. The actor must already be running.
func (a *Actor) SendTask(task protocol.Task) error {
	tc := &taskContext{
		TaskID:    task.TaskID,
		StartedAt: time.Now(),
	}
	a.taskInFlight.Store(tc)

	if err := a.opts.Relay.Send(&protocol.TaskStarted{
		Type:      protocol.MsgTypeTaskStarted,
		TaskID:    task.TaskID,
		SessionID: a.opts.SessionID,
		ChannelID: a.opts.ChannelID,
		StartedAt: tc.StartedAt.UTC().Format(time.RFC3339Nano),
	}); err != nil {
		return fmt.Errorf("send taskStarted: %w", err)
	}

	return a.executor.Send(task.Prompt)
}

// Run starts the Claude process and forwards events to the relay.
// Blocks until ctx is canceled or Stop is called.
func (a *Actor) Run(ctx context.Context) error {
	stopCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-a.stopCh:
			cancel()
		case <-stopCtx.Done():
		}
	}()

	return a.executor.Start(stopCtx, func(e claude.Event) error {
		return a.handleEvent(e)
	})
}

// handleEvent assigns a sequence number, writes to WAL, and pushes to relay.
func (a *Actor) handleEvent(e claude.Event) error {
	next := a.seq.Add(1)

	// Every event becomes a stream frame
	frame := &protocol.Stream{
		Type:           protocol.MsgTypeStream,
		SessionID:      a.opts.SessionID,
		ChannelID:      a.opts.ChannelID,
		SequenceNumber: next,
		Event:          e.Raw,
	}

	frameJSON, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("marshal frame: %w", err)
	}
	if err := a.log.Append(next, frameJSON); err != nil {
		return fmt.Errorf("wal append: %w", err)
	}
	if err := a.opts.Relay.Send(frame); err != nil {
		// Best-effort — WAL has the entry, relay reconnect will replay it
		return nil
	}

	// On result events, also emit taskComplete
	if e.Type == "result" {
		return a.handleResult(e.Raw)
	}
	return nil
}

func (a *Actor) handleResult(raw json.RawMessage) error {
	tc, ok := a.taskInFlight.Load().(*taskContext)
	if !ok || tc == nil {
		return nil
	}

	var payload struct {
		SessionID    string  `json:"session_id"`
		TotalCostUSD float64 `json:"total_cost_usd"`
		DurationMs   int     `json:"duration_ms"`
		Usage        struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			CacheReadInput     int `json:"cache_read_input_tokens"`
			CacheCreationInput int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(raw, &payload)

	if payload.SessionID != "" {
		a.claudeSessionID.Store(payload.SessionID)
	}

	cost := fmt.Sprintf("%.6f", payload.TotalCostUSD)
	complete := &protocol.TaskComplete{
		Type:            protocol.MsgTypeTaskComplete,
		TaskID:          tc.TaskID,
		SessionID:       a.opts.SessionID,
		ChannelID:       a.opts.ChannelID,
		ClaudeSessionID: payload.SessionID,
		InputTokens: int64(
			payload.Usage.InputTokens +
				payload.Usage.CacheReadInput +
				payload.Usage.CacheCreationInput,
		),
		OutputTokens: int64(payload.Usage.OutputTokens),
		CostUSD:      cost,
		DurationMs:   payload.DurationMs,
	}

	a.taskInFlight.Store((*taskContext)(nil))
	return a.opts.Relay.Send(complete)
}

// Stop closes the Claude process and the WAL.
func (a *Actor) Stop() error {
	a.stopOnce.Do(func() {
		close(a.stopCh)
	})
	if a.executor != nil {
		_ = a.executor.Close()
	}
	return a.log.Close()
}

// PruneWAL removes WAL entries up to (and including) upTo.
func (a *Actor) PruneWAL(upTo int64) error {
	return a.log.PruneUpTo(upTo)
}
