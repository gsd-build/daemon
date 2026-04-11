// Package session ties the Claude executor and relay together
// into one "session actor" per user session.
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gsd-build/daemon/internal/claude"
	"github.com/gsd-build/daemon/internal/pidfile"
	"github.com/gsd-build/daemon/internal/sockapi"
	protocol "github.com/gsd-build/protocol-go"
)

// RelaySender is the minimal interface the actor needs to push events to the relay.
type RelaySender interface {
	Send(ctx context.Context, msg any) error
}

// Options configures a new Actor.
type Options struct {
	SessionID      string
	BinaryPath     string
	CWD            string
	Relay          RelaySender
	Model          string
	Effort         string
	PermissionMode string
	SystemPrompt   string
	ResumeSession  string
}

// Actor drives a single Claude session using spawn-per-task execution.
// Each incoming task spawns a fresh claude process; no processes remain
// alive between tasks.
type Actor struct {
	opts Options

	seq int64 // monotonic sequence counter, only touched by Run goroutine

	claudeSessionID string   // set from result events, used for --resume
	allowedTools    []string // accumulates as user grants permissions

	taskCh chan protocol.Task // SendTask writes here, Run reads
	permCh chan permResponse  // HandlePermissionResponse writes here

	// Question responses are routed by requestId so batch questions can
	// be answered in any order without blocking each other.
	questionMu sync.Mutex
	questionCh map[string]chan string // requestId → answer channel

	stopCh chan struct{}

	taskMu     sync.Mutex
	taskCancel context.CancelFunc // cancels the in-flight task context; nil when idle
	taskID     string             // ID of the in-flight task; empty when idle

	taskStartedAt *time.Time // when current task started; nil when idle
	idleSince     *time.Time // when actor became idle; nil when executing

	// taskTimeout is the per-task deadline. Zero means no timeout.
	// Set by the Manager from config before calling Run.
	taskTimeout time.Duration

	// lastActiveAt tracks when this actor last completed or received a task.
	// Protected by taskMu. Used by the reaper to detect idle actors.
	lastActiveAt time.Time

	// pidDir is the directory for PID files. Empty disables PID tracking.
	pidDir string
}

type taskContext struct {
	TaskID         string
	ChannelID      string
	StartedAt      time.Time
	OriginalPrompt string
}

// pendingDenial tracks a task waiting on permission/question responses.
type pendingDenial struct {
	Denials []string
	TaskID  string
	Prompt  string
}

type permResponse struct {
	Approved bool
	ToolName string // for permission grants
	Answer   string // for question answers
}

// NewActor creates a new Actor for the given session.
func NewActor(opts Options) (*Actor, error) {
	return &Actor{
		opts:            opts,
		claudeSessionID: opts.ResumeSession,
		taskCh:          make(chan protocol.Task, 1),
		permCh:          make(chan permResponse, 1),
		questionCh:      make(map[string]chan string),
		stopCh:          make(chan struct{}),
		lastActiveAt:    time.Now(),
	}, nil
}

// LastActiveAt returns the time of the actor's last task completion or creation.
func (a *Actor) LastActiveAt() time.Time {
	a.taskMu.Lock()
	defer a.taskMu.Unlock()
	return a.lastActiveAt
}

// HasInFlightTask returns true if the actor is currently executing a task.
func (a *Actor) HasInFlightTask() bool {
	a.taskMu.Lock()
	defer a.taskMu.Unlock()
	return a.taskID != ""
}

// Info returns a snapshot of the actor's current state for the status API.
func (a *Actor) Info() sockapi.SessionInfo {
	a.taskMu.Lock()
	defer a.taskMu.Unlock()

	info := sockapi.SessionInfo{
		SessionID: a.opts.SessionID,
	}
	if a.taskID != "" {
		info.State = "executing"
		info.TaskID = a.taskID
		info.StartedAt = a.taskStartedAt
	} else {
		info.State = "idle"
		info.IdleSince = a.idleSince
	}
	return info
}

// InFlightTaskID returns the ID of the currently executing task, or "" if idle.
func (a *Actor) InFlightTaskID() string {
	a.taskMu.Lock()
	defer a.taskMu.Unlock()
	return a.taskID
}

// AllowedTools returns the current granted tools list.
func (a *Actor) AllowedTools() []string {
	out := make([]string, len(a.allowedTools))
	copy(out, a.allowedTools)
	return out
}

// SendTask queues a task for execution. Non-blocking if the channel has capacity.
func (a *Actor) SendTask(task protocol.Task) error {
	select {
	case a.taskCh <- task:
		return nil
	default:
		return fmt.Errorf("actor busy — task channel full")
	}
}

// Run is the actor's main loop. It waits for tasks, spawns executors, and
// handles permission flows. Blocks until ctx is canceled or Stop is called.
func (a *Actor) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-a.stopCh:
			return nil
		case task := <-a.taskCh:
			if err := a.executeTask(ctx, task); err != nil {
				log.Printf("[actor] task %s failed: %v", task.TaskID, err)
				sendCtx, sendCancel := context.WithTimeout(ctx, 30*time.Second)
				_ = a.opts.Relay.Send(sendCtx, &protocol.TaskError{
					Type:      protocol.MsgTypeTaskError,
					TaskID:    task.TaskID,
					SessionID: a.opts.SessionID,
					ChannelID: task.ChannelID,
					Error:     err.Error(),
				})
				sendCancel()
			}
		}
	}
}

func (a *Actor) executeTask(ctx context.Context, task protocol.Task) error {
	var taskCtx context.Context
	var cancel context.CancelFunc

	if a.taskTimeout > 0 {
		taskCtx, cancel = context.WithTimeout(ctx, a.taskTimeout)
	} else {
		taskCtx, cancel = context.WithCancel(ctx)
	}

	a.taskMu.Lock()
	a.taskCancel = cancel
	a.taskID = task.TaskID
	now := time.Now()
	a.taskStartedAt = &now
	a.idleSince = nil
	a.taskMu.Unlock()

	defer func() {
		cancel()
		a.taskMu.Lock()
		a.taskCancel = nil
		a.taskID = ""
		a.taskStartedAt = nil
		idleNow := time.Now()
		a.idleSince = &idleNow
		a.lastActiveAt = idleNow
		a.taskMu.Unlock()
	}()

	tc := &taskContext{
		TaskID:         task.TaskID,
		ChannelID:      task.ChannelID,
		StartedAt:      time.Now(),
		OriginalPrompt: task.Prompt,
	}

	slog.Info("task received", "task", task.TaskID, "session", a.opts.SessionID, "prompt", truncate(task.Prompt, 80))

	sendCtx, sendCancel := context.WithTimeout(ctx, 30*time.Second)
	if err := a.opts.Relay.Send(sendCtx, &protocol.TaskStarted{
		Type:      protocol.MsgTypeTaskStarted,
		TaskID:    task.TaskID,
		SessionID: a.opts.SessionID,
		ChannelID: tc.ChannelID,
		StartedAt: tc.StartedAt.UTC().Format(time.RFC3339Nano),
	}); err != nil {
		sendCancel()
		return fmt.Errorf("send taskStarted: %w", err)
	}
	sendCancel()

	err := a.runExecutor(taskCtx, tc, task.Prompt)

	// If the task context was cancelled (user hit ESC or timeout), send the
	// appropriate message and loop back for the next task.
	if taskCtx.Err() != nil && ctx.Err() == nil {
		if taskCtx.Err() == context.DeadlineExceeded {
			errCtx, errCancel := context.WithTimeout(ctx, 30*time.Second)
			_ = a.opts.Relay.Send(errCtx, &protocol.TaskError{
				Type:      protocol.MsgTypeTaskError,
				TaskID:    task.TaskID,
				SessionID: a.opts.SessionID,
				ChannelID: tc.ChannelID,
				Error:     fmt.Sprintf("task timed out after %s", a.taskTimeout),
			})
			errCancel()
			return nil
		}
		cancelCtx, cancelCancel := context.WithTimeout(ctx, 30*time.Second)
		_ = a.opts.Relay.Send(cancelCtx, &protocol.TaskCancelled{
			Type:      protocol.MsgTypeTaskCancelled,
			TaskID:    task.TaskID,
			SessionID: a.opts.SessionID,
			ChannelID: tc.ChannelID,
		})
		cancelCancel()
		return nil
	}

	return err
}

func (a *Actor) runExecutor(ctx context.Context, tc *taskContext, prompt string) error {
	exec := claude.NewExecutor(claude.Options{
		BinaryPath:     a.opts.BinaryPath,
		CWD:            a.opts.CWD,
		Model:          a.opts.Model,
		Effort:         a.opts.Effort,
		PermissionMode: a.opts.PermissionMode,
		SystemPrompt:   a.opts.SystemPrompt,
		ResumeSession:  a.claudeSessionID,
		AllowedTools:   a.allowedTools,
		Prompt:         prompt,
	})

	if a.pidDir != "" {
		exec.OnPIDStart = func(pid int) {
			path := filepath.Join(a.pidDir, fmt.Sprintf("%s.pid", tc.TaskID))
			if err := pidfile.Write(path, pid); err != nil {
				log.Printf("[actor] write pid file: %v", err)
			}
		}
		exec.OnPIDExit = func(pid int) {
			path := filepath.Join(a.pidDir, fmt.Sprintf("%s.pid", tc.TaskID))
			pidfile.Remove(path)
		}
	}

	var resultRaw json.RawMessage

	err := exec.Run(ctx, func(e claude.Event) error {
		a.seq++
		next := a.seq

		// Send to relay
		frame := &protocol.Stream{
			Type:           protocol.MsgTypeStream,
			SessionID:      a.opts.SessionID,
			ChannelID:      tc.ChannelID,
			SequenceNumber: next,
			Event:          e.Raw,
		}
		sendCtx, sendCancel := context.WithTimeout(ctx, 5*time.Second)
		if err := a.opts.Relay.Send(sendCtx, frame); err != nil {
			log.Printf("[actor] relay send failed: session=%s seq=%d err=%v",
				a.opts.SessionID, next, err)
		}
		sendCancel()

		if e.Type == "result" {
			resultRaw = make([]byte, len(e.Raw))
			copy(resultRaw, e.Raw)
		}
		return nil
	})

	if err != nil {
		return err
	}

	if resultRaw == nil {
		return fmt.Errorf("executor exited without result event")
	}

	return a.handleResult(ctx, tc, resultRaw)
}

func (a *Actor) handleResult(ctx context.Context, tc *taskContext, raw json.RawMessage) error {
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
		a.claudeSessionID = payload.SessionID
	}

	// Permission denials — wait for response then re-spawn
	if len(payload.PermissionDenials) > 0 {
		return a.handleDenials(ctx, tc, payload.PermissionDenials)
	}

	// Normal completion
	cost := fmt.Sprintf("%.6f", payload.TotalCostUSD)
	sendCtx, sendCancel := context.WithTimeout(ctx, 30*time.Second)
	defer sendCancel()
	return a.opts.Relay.Send(sendCtx, &protocol.TaskComplete{
		Type:            protocol.MsgTypeTaskComplete,
		TaskID:          tc.TaskID,
		SessionID:       a.opts.SessionID,
		ChannelID:       tc.ChannelID,
		ClaudeSessionID: payload.SessionID,
		InputTokens: int64(
			payload.Usage.InputTokens +
				payload.Usage.CacheReadInput +
				payload.Usage.CacheCreationInput,
		),
		OutputTokens: int64(payload.Usage.OutputTokens),
		CostUSD:      cost,
		DurationMs:   payload.DurationMs,
	})
}

// questionDenial tracks a single AskUserQuestion denial awaiting batch dispatch.
type questionDenial struct {
	RequestID string
	ToolInput json.RawMessage
}

func (a *Actor) handleDenials(ctx context.Context, tc *taskContext, denials []struct {
	ToolName  string          `json:"tool_name"`
	ToolUseID string          `json:"tool_use_id"`
	ToolInput json.RawMessage `json:"tool_input"`
}) error {
	// Separate questions from permission requests so questions can be
	// sent as a batch and answered in any order.
	var questions []questionDenial
	var permDenials []struct {
		ToolName  string          `json:"tool_name"`
		ToolUseID string          `json:"tool_use_id"`
		ToolInput json.RawMessage `json:"tool_input"`
	}

	for _, denial := range denials {
		if denial.ToolName == "AskUserQuestion" {
			questions = append(questions, questionDenial{
				RequestID: denial.ToolUseID,
				ToolInput: denial.ToolInput,
			})
		} else {
			permDenials = append(permDenials, denial)
		}
	}

	// Handle batch questions first — send all, collect all, re-spawn once.
	if len(questions) > 0 {
		return a.handleBatchQuestions(ctx, tc, questions)
	}

	// Permission requests are still handled one at a time since each
	// approval/denial changes the executor's allowed-tools set.
	for _, denial := range permDenials {
		sendCtx, sendCancel := context.WithTimeout(ctx, 30*time.Second)
		if err := a.opts.Relay.Send(sendCtx, &protocol.PermissionRequest{
			Type:      protocol.MsgTypePermissionRequest,
			SessionID: a.opts.SessionID,
			ChannelID: tc.ChannelID,
			RequestID: denial.ToolUseID,
			ToolName:  denial.ToolName,
			ToolInput: denial.ToolInput,
		}); err != nil {
			sendCancel()
			return err
		}
		sendCancel()
		summary, _ := toolInputSummary(denial.ToolInput)
		slog.Info("permission request sent", "tool", denial.ToolName, "summary", summary)

		// Wait for response
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-a.stopCh:
			return fmt.Errorf("actor stopped while waiting for permission")
		case resp := <-a.permCh:
			if resp.Approved {
				slog.Info("permission approved, resuming", "tool", denial.ToolName)
				a.addAllowedTool(denial.ToolName)
				return a.runExecutor(ctx, tc, tc.OriginalPrompt)
			}
			return a.handleDenyResponse(ctx, tc)
		}
	}
	return nil
}

// parsedQuestion holds the extracted fields from a single AskUserQuestion denial.
type parsedQuestion struct {
	RequestID string
	Text      string
	Options   []string
}

// parseQuestionDenial extracts questions from an AskUserQuestion tool_input.
// Claude may pack multiple questions into a single tool call's questions array.
func parseQuestionDenial(requestID string, toolInput json.RawMessage) []parsedQuestion {
	var qPayload struct {
		Questions []struct {
			Question string `json:"question"`
			Options  []struct {
				Label string `json:"label"`
			} `json:"options"`
		} `json:"questions"`
	}
	_ = json.Unmarshal(toolInput, &qPayload)

	if len(qPayload.Questions) == 0 {
		return nil
	}

	// If there's only one question, use the denial's requestID directly.
	if len(qPayload.Questions) == 1 {
		q := qPayload.Questions[0]
		var opts []string
		for _, o := range q.Options {
			opts = append(opts, o.Label)
		}
		return []parsedQuestion{{RequestID: requestID, Text: q.Question, Options: opts}}
	}

	// Multiple questions in one denial — synthesize sub-requestIDs.
	var out []parsedQuestion
	for i, q := range qPayload.Questions {
		var opts []string
		for _, o := range q.Options {
			opts = append(opts, o.Label)
		}
		out = append(out, parsedQuestion{
			RequestID: fmt.Sprintf("%s_%d", requestID, i),
			Text:      q.Question,
			Options:   opts,
		})
	}
	return out
}

// handleBatchQuestions sends all questions to the relay at once, waits for
// all answers (in any order), then re-spawns the executor with a combined prompt.
func (a *Actor) handleBatchQuestions(ctx context.Context, tc *taskContext, denials []questionDenial) error {
	// Parse all questions from all denials.
	var allQuestions []parsedQuestion
	for _, d := range denials {
		allQuestions = append(allQuestions, parseQuestionDenial(d.RequestID, d.ToolInput)...)
	}

	if len(allQuestions) == 0 {
		return fmt.Errorf("AskUserQuestion denial with no parseable questions")
	}

	// Register answer channels before sending so responses aren't lost.
	a.questionMu.Lock()
	for _, q := range allQuestions {
		a.questionCh[q.RequestID] = make(chan string, 1)
	}
	a.questionMu.Unlock()

	// Clean up channels when done, regardless of outcome.
	defer func() {
		a.questionMu.Lock()
		for _, q := range allQuestions {
			delete(a.questionCh, q.RequestID)
		}
		a.questionMu.Unlock()
	}()

	// Send all questions to relay at once.
	for _, q := range allQuestions {
		sendCtx, sendCancel := context.WithTimeout(ctx, 30*time.Second)
		if err := a.opts.Relay.Send(sendCtx, &protocol.Question{
			Type:      protocol.MsgTypeQuestion,
			SessionID: a.opts.SessionID,
			ChannelID: tc.ChannelID,
			RequestID: q.RequestID,
			Question:  q.Text,
			Options:   q.Options,
		}); err != nil {
			sendCancel()
			return err
		}
		sendCancel()
		slog.Info("question sent", "requestID", q.RequestID, "question", q.Text)
	}

	slog.Debug("waiting for answers", "count", len(allQuestions))

	// Collect all answers. Order doesn't matter — each channel is keyed by requestID.
	answers := make(map[string]string, len(allQuestions))
	for _, q := range allQuestions {
		ch := a.questionCh[q.RequestID]
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-a.stopCh:
			return fmt.Errorf("actor stopped while waiting for answers")
		case answer := <-ch:
			answers[q.RequestID] = answer
			slog.Info("answer received", "requestID", q.RequestID, "answer", answer)
		}
	}

	// Compose a single prompt with all answers.
	var parts []string
	for _, q := range allQuestions {
		parts = append(parts, fmt.Sprintf("Q: %s\nA: %s", q.Text, answers[q.RequestID]))
	}
	answerPrompt := "My answers:\n\n" + strings.Join(parts, "\n\n")

	return a.runExecutor(ctx, tc, answerPrompt)
}

func (a *Actor) handleDenyResponse(ctx context.Context, tc *taskContext) error {
	denyPrompt := "The previous tool request was denied. Please continue without using that tool."
	return a.runExecutor(ctx, tc, denyPrompt)
}

// HandlePermissionResponse processes a permission response from the relay.
func (a *Actor) HandlePermissionResponse(resp *protocol.PermissionResponse) error {
	select {
	case a.permCh <- permResponse{Approved: resp.Approved}:
		return nil
	default:
		return fmt.Errorf("no pending permission request for session %s", a.opts.SessionID)
	}
}

// HandleQuestionResponse routes an answer to the waiting question by requestId.
func (a *Actor) HandleQuestionResponse(resp *protocol.QuestionResponse) error {
	a.questionMu.Lock()
	ch, ok := a.questionCh[resp.RequestID]
	a.questionMu.Unlock()
	if !ok {
		return fmt.Errorf("no pending question %s for session %s", resp.RequestID, a.opts.SessionID)
	}
	select {
	case ch <- resp.Answer:
		return nil
	default:
		return fmt.Errorf("question %s already answered for session %s", resp.RequestID, a.opts.SessionID)
	}
}

func (a *Actor) addAllowedTool(name string) {
	for _, t := range a.allowedTools {
		if t == name {
			return
		}
	}
	a.allowedTools = append(a.allowedTools, name)
}

// GetClaudeSessionID returns the most recent Claude session id.
func (a *Actor) GetClaudeSessionID() string {
	return a.claudeSessionID
}

// Stop signals the actor to shut down.
func (a *Actor) Stop() error {
	select {
	case <-a.stopCh:
	default:
		close(a.stopCh)
	}
	return nil
}

// CancelTask cancels the in-flight task (if any) without shutting down the actor.
// The executor's context.Done fires, SIGKILLing the Claude subprocess.
// Run() loops back and waits for the next task.
func (a *Actor) CancelTask() {
	a.taskMu.Lock()
	defer a.taskMu.Unlock()
	if a.taskCancel != nil {
		a.taskCancel()
		a.taskCancel = nil
	}
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}

func toolInputSummary(raw json.RawMessage) (string, bool) {
	var input map[string]interface{}
	if json.Unmarshal(raw, &input) != nil {
		return "", false
	}
	if cmd, ok := input["command"].(string); ok {
		return "command: " + cmd, true
	}
	if fp, ok := input["file_path"].(string); ok {
		return "file_path: " + fp, true
	}
	return "", false
}
