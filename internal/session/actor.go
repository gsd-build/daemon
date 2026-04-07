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
	pendingDenial atomic.Value // *pendingDenial

	stopOnce sync.Once
	stopCh   chan struct{}

	claudeSessionID atomic.Value // string
	allowedTools    []string     // accumulates per session as user grants permissions
}

type taskContext struct {
	TaskID         string
	StartedAt      time.Time
	OriginalPrompt string
	Input          int64
	Output         int64
	CostUSD        string
}

// pendingDenial tracks a task that's waiting on permission/question responses.
type pendingDenial struct {
	Denials []string // tool names being awaited
	TaskID  string
	Prompt  string
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
	if pd := a.pendingDenial.Load(); pd != nil {
		if v, ok := pd.(*pendingDenial); ok && v != nil {
			return fmt.Errorf("session is awaiting a permission/question response; cannot accept new task")
		}
	}

	tc := &taskContext{
		TaskID:         task.TaskID,
		StartedAt:      time.Now(),
		OriginalPrompt: task.Prompt,
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
		PermissionDenials []struct {
			ToolName  string          `json:"tool_name"`
			ToolUseID string          `json:"tool_use_id"`
			ToolInput json.RawMessage `json:"tool_input"`
		} `json:"permission_denials"`
	}
	_ = json.Unmarshal(raw, &payload)

	if payload.SessionID != "" {
		a.claudeSessionID.Store(payload.SessionID)
	}

	// If there are permission denials, synthesize PermissionRequest / Question
	// envelopes and DO NOT complete the task. The actor enters a waiting state.
	if len(payload.PermissionDenials) > 0 {
		for _, denial := range payload.PermissionDenials {
			if denial.ToolName == "AskUserQuestion" {
				var qPayload struct {
					Questions []struct {
						Question string `json:"question"`
						Header   string `json:"header"`
						Options  []struct {
							Label       string `json:"label"`
							Description string `json:"description"`
						} `json:"options"`
					} `json:"questions"`
				}
				_ = json.Unmarshal(denial.ToolInput, &qPayload)
				var questionText string
				var optionLabels []string
				if len(qPayload.Questions) > 0 {
					questionText = qPayload.Questions[0].Question
					for _, opt := range qPayload.Questions[0].Options {
						optionLabels = append(optionLabels, opt.Label)
					}
				}

				if err := a.opts.Relay.Send(&protocol.Question{
					Type:      protocol.MsgTypeQuestion,
					SessionID: a.opts.SessionID,
					ChannelID: a.opts.ChannelID,
					RequestID: denial.ToolUseID,
					Question:  questionText,
					Options:   optionLabels,
				}); err != nil {
					return err
				}
			} else {
				if err := a.opts.Relay.Send(&protocol.PermissionRequest{
					Type:      protocol.MsgTypePermissionRequest,
					SessionID: a.opts.SessionID,
					ChannelID: a.opts.ChannelID,
					RequestID: denial.ToolUseID,
					ToolName:  denial.ToolName,
					ToolInput: denial.ToolInput,
				}); err != nil {
					return err
				}
			}
		}

		// Mark waiting on a permission response.
		a.pendingDenial.Store(&pendingDenial{
			Denials: denialNames(payload.PermissionDenials),
			TaskID:  tc.TaskID,
			Prompt:  tc.OriginalPrompt,
		})
		// IMPORTANT: do NOT emit TaskComplete yet.
		return nil
	}

	// No denials — emit TaskComplete normally.
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

func denialNames(denials []struct {
	ToolName  string          `json:"tool_name"`
	ToolUseID string          `json:"tool_use_id"`
	ToolInput json.RawMessage `json:"tool_input"`
}) []string {
	out := make([]string, 0, len(denials))
	for _, d := range denials {
		out = append(out, d.ToolName)
	}
	return out
}

// GetClaudeSessionID returns the most recent Claude session id observed.
func (a *Actor) GetClaudeSessionID() string {
	v := a.claudeSessionID.Load()
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// RestartWithGrant kills the current executor and spawns a fresh one with the
// new tool added to the allow list and --resume to the existing claude session.
// Then re-sends the original prompt that triggered the denial.
func (a *Actor) RestartWithGrant(ctx context.Context, toolName, originalPrompt string) error {
	// Add the tool (dedup)
	already := false
	for _, t := range a.allowedTools {
		if t == toolName {
			already = true
			break
		}
	}
	if !already {
		a.allowedTools = append(a.allowedTools, toolName)
	}

	// Snapshot the claude session id
	claudeSess := a.GetClaudeSessionID()
	if claudeSess == "" {
		return fmt.Errorf("no claude session id to resume")
	}

	// Close the old executor
	if a.executor != nil {
		_ = a.executor.Close()
	}

	// Build a new executor with --resume + --allowedTools
	a.executor = claude.NewExecutor(claude.Options{
		BinaryPath:     a.opts.BinaryPath,
		CWD:            a.opts.CWD,
		Model:          a.opts.Model,
		Effort:         a.opts.Effort,
		PermissionMode: a.opts.PermissionMode,
		SystemPrompt:   a.opts.SystemPrompt,
		ResumeSession:  claudeSess,
		AllowedTools:   append([]string{}, a.allowedTools...),
	})

	// Start the new process in a goroutine
	go func() {
		_ = a.executor.Start(ctx, func(e claude.Event) error {
			return a.handleEvent(e)
		})
	}()

	// Give the process a moment to spin up
	time.Sleep(500 * time.Millisecond)

	// Re-send the original prompt
	return a.executor.Send(originalPrompt)
}

// HandlePermissionResponse processes a permission response from the relay.
// On Approve: restarts the executor with --allowedTools + --resume and re-sends the original prompt.
// On Deny: sends a follow-up user message saying the request was denied.
func (a *Actor) HandlePermissionResponse(resp *protocol.PermissionResponse) error {
	pdAny := a.pendingDenial.Load()
	pd, ok := pdAny.(*pendingDenial)
	if !ok || pd == nil {
		return fmt.Errorf("no pending denial for session %s", a.opts.SessionID)
	}

	if resp.Approved {
		a.pendingDenial.Store((*pendingDenial)(nil))
		if len(pd.Denials) == 0 {
			return fmt.Errorf("pending denial has no tool names")
		}
		return a.RestartWithGrant(context.Background(), pd.Denials[0], pd.Prompt)
	}

	return a.handleDeny(pd, "user denied the permission request")
}

// HandleQuestionResponse processes an answer to a question.
func (a *Actor) HandleQuestionResponse(resp *protocol.QuestionResponse) error {
	pdAny := a.pendingDenial.Load()
	pd, ok := pdAny.(*pendingDenial)
	if !ok || pd == nil {
		return fmt.Errorf("no pending question for session %s", a.opts.SessionID)
	}
	_ = pd // we don't need pd state for the answer flow

	a.pendingDenial.Store((*pendingDenial)(nil))
	answerMsg := fmt.Sprintf("My answer: %s", resp.Answer)
	if err := a.executor.Send(answerMsg); err != nil {
		return fmt.Errorf("send answer to claude: %w", err)
	}

	tc, _ := a.taskInFlight.Load().(*taskContext)
	if tc != nil {
		_ = a.opts.Relay.Send(&protocol.TaskComplete{
			Type:            protocol.MsgTypeTaskComplete,
			TaskID:          tc.TaskID,
			SessionID:       a.opts.SessionID,
			ChannelID:       a.opts.ChannelID,
			ClaudeSessionID: a.GetClaudeSessionID(),
		})
		a.taskInFlight.Store((*taskContext)(nil))
	}

	return nil
}

func (a *Actor) handleDeny(pd *pendingDenial, reason string) error {
	a.pendingDenial.Store((*pendingDenial)(nil))
	denyMsg := fmt.Sprintf("The previous tool request was denied: %s. Please continue without using that tool.", reason)
	if err := a.executor.Send(denyMsg); err != nil {
		return err
	}

	tc, _ := a.taskInFlight.Load().(*taskContext)
	if tc != nil {
		_ = a.opts.Relay.Send(&protocol.TaskComplete{
			Type:            protocol.MsgTypeTaskComplete,
			TaskID:          tc.TaskID,
			SessionID:       a.opts.SessionID,
			ChannelID:       a.opts.ChannelID,
			ClaudeSessionID: a.GetClaudeSessionID(),
		})
		a.taskInFlight.Store((*taskContext)(nil))
	}
	return nil
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
