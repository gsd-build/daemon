// Package session ties task execution and relay forwarding together
// into one "session actor" per user session.
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gsd-build/daemon/internal/claude"
	daemonfs "github.com/gsd-build/daemon/internal/fs"
	"github.com/gsd-build/daemon/internal/pi"
	"github.com/gsd-build/daemon/internal/pidfile"
	"github.com/gsd-build/daemon/internal/skills"
	"github.com/gsd-build/daemon/internal/sockapi"
	"github.com/gsd-build/daemon/internal/upload"
	protocol "github.com/gsd-build/protocol-go"
)

// RelaySender is the minimal interface the actor needs to push events to the relay.
type RelaySender interface {
	Send(ctx context.Context, msg any) error
}

// ImageUploader uploads an image file to the relay and returns a public URL.
type ImageUploader interface {
	Upload(ctx context.Context, filename string, data []byte) (string, error)
}

// Options configures a new Actor.
type Options struct {
	SessionID         string
	CWD               string
	Relay             RelaySender
	Model             string
	Effort            string
	PermissionMode    string
	WarmPiWorkers     bool
	WarmClaudeSDK     bool
	ResumeSession     string
	PiBinaryPath      string
	PiExtensionPath   string
	Uploader          ImageUploader // nil = image upload disabled
	BrowserGrantID    string
	BrowserID         string
	RecordTouchedFile func(channelID string, cwd string, path string)
	OnTaskIdle        func()
}

// Actor drives a single agent session using spawn-per-task execution.
// Each incoming task spawns a fresh executor process; no processes remain
// alive between tasks.
type Actor struct {
	opts Options

	seq int64 // monotonic sequence counter, advanced via atomic ops

	claudeSessionID string   // normalized result session id used in protocol responses
	allowedTools    []string // accumulates as user grants permissions

	taskCh chan protocol.Task // SendTask writes here, Run reads
	permCh chan permResponse  // HandlePermissionResponse writes here

	// Question responses are routed by requestId so batch questions can
	// be answered in any order without blocking each other.
	questionMu sync.Mutex
	questionCh map[string]chan string // requestId → answer channel

	stopCh chan struct{}

	taskMu        sync.Mutex
	taskCancel    context.CancelFunc // cancels the in-flight task context; nil when idle
	taskID        string             // ID of the in-flight task; empty when idle
	pendingTaskID string             // ID of a task accepted into taskCh but not yet started

	taskStartedAt *time.Time // when current task started; nil when idle
	idleSince     *time.Time // when actor became idle; nil when executing
	piModel       string     // Pi model used for context-window fallbacks

	// taskTimeout is the per-task deadline. Zero means no timeout.
	// Set by the Manager from config before calling Run.
	taskTimeout time.Duration
	// interactionTimeout bounds permission/question waits. Zero falls back
	// to the default 10-minute ceiling.
	interactionTimeout time.Duration

	// lastActiveAt tracks when this actor last completed or received a task.
	// Protected by taskMu. Used by the reaper to detect idle actors.
	lastActiveAt time.Time

	// pidDir is the directory for PID files. Empty disables PID tracking.
	pidDir string

	piWorkerMu      sync.Mutex
	piWorker        *pi.Worker
	piWorkerKey     pi.WorkerKey
	useWarmPiWorker bool

	// pendingFileToolStarts tracks pi file tool_execution_start events awaiting
	// their matching tool_execution_end. Keyed by toolCallID, value is
	// pendingFileTool. Populated in capturePiToolStart and consumed in
	// capturePiToolEnd.
	pendingFileToolStarts sync.Map
	localServerDetections sync.Map

	runPiControl func(ctx context.Context, command pi.ControlCommand, onEvent func(pi.ControlEvent)) (pi.ControlResult, error)
	now          func() time.Time
}

// pendingFileTool records a pi file-tool start event awaiting its matching
// tool_execution_end.
type pendingFileTool struct {
	toolName  string
	args      map[string]any
	startedAt time.Time
}

type taskContext struct {
	TaskID             string
	ChannelID          string
	ActorContext       context.Context
	StartedAt          time.Time
	OriginalPrompt     string
	ExecutionPrompt    string
	Engine             string
	Provider           string
	Model              string
	Effort             string
	PermissionMode     string
	RequestID          string
	Traceparent        string
	ImageURLs          []string
	ContextRefs        []protocol.ContextRef
	CustomInstructions string
	DisableSkills      bool
	BrowserGrantID     string
	BrowserID          string
	PlanCapability     *protocol.PlanCapability
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

const defaultInteractionTimeout = 10 * time.Minute
const piReserveTokens int64 = 16384
const piKeepRecentTokens int64 = 20000

// NewActor creates a new Actor for the given session.
func NewActor(opts Options) (*Actor, error) {
	actor := &Actor{
		opts:               opts,
		claudeSessionID:    opts.ResumeSession,
		taskCh:             make(chan protocol.Task, 1),
		permCh:             make(chan permResponse, 1),
		questionCh:         make(map[string]chan string),
		stopCh:             make(chan struct{}),
		lastActiveAt:       time.Now(),
		interactionTimeout: defaultInteractionTimeout,
		now:                time.Now,
	}
	actor.useWarmPiWorker = warmPiWorkersEnabled(opts.WarmPiWorkers)
	actor.opts.WarmClaudeSDK = warmClaudeSDKEnabled(opts.WarmClaudeSDK && actor.useWarmPiWorker)
	actor.runPiControl = func(ctx context.Context, command pi.ControlCommand, onEvent func(pi.ControlEvent)) (pi.ControlResult, error) {
		sessionFile, err := piSessionFileForSession(actor.opts.SessionID)
		if err != nil {
			return pi.ControlResult{}, err
		}
		binaryPath := actor.opts.PiBinaryPath
		if binaryPath == "" {
			binaryPath = "pi"
		}
		return pi.RunControl(ctx, pi.ControlOptions{
			BinaryPath:  binaryPath,
			CWD:         actor.opts.CWD,
			SessionFile: sessionFile,
			Model:       actor.currentPiModel(),
			Command:     command,
			OnEvent:     onEvent,
		})
	}
	return actor, nil
}

func warmPiWorkersEnabled(defaultEnabled bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GSD_WARM_PI_WORKERS"))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return defaultEnabled
	}
}

func warmClaudeSDKEnabled(defaultEnabled bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GSD_WARM_CLAUDE_SDK"))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return defaultEnabled
	}
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

// HasBeenIdle returns true if the actor has completed at least one task and
// is now idle. A freshly spawned actor that hasn't executed yet returns false.
func (a *Actor) HasBeenIdle() bool {
	a.taskMu.Lock()
	defer a.taskMu.Unlock()
	return a.idleSince != nil
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

func (a *Actor) workerPIDForTest() int {
	a.piWorkerMu.Lock()
	defer a.piWorkerMu.Unlock()
	if a.piWorker == nil {
		return 0
	}
	return a.piWorker.PID()
}

func (a *Actor) stopPiWorker(ctx context.Context) {
	a.piWorkerMu.Lock()
	worker := a.piWorker
	a.piWorker = nil
	a.piWorkerMu.Unlock()
	if worker != nil {
		_ = worker.Stop(ctx)
	}
}

func (a *Actor) WorkerSnapshot() (pi.WorkerSnapshot, bool) {
	a.piWorkerMu.Lock()
	defer a.piWorkerMu.Unlock()
	if a.piWorker == nil {
		return pi.WorkerSnapshot{}, false
	}
	return a.piWorker.Snapshot(a.opts.SessionID), true
}

func (a *Actor) StopIdleWorker(ctx context.Context, reason string) bool {
	a.piWorkerMu.Lock()
	worker := a.piWorker
	if worker == nil || !worker.IsIdle() {
		a.piWorkerMu.Unlock()
		return false
	}
	a.piWorker = nil
	a.piWorkerMu.Unlock()
	_ = worker.Stop(ctx)
	slog.Info("pi_worker_evict", "sessionId", a.opts.SessionID, "reason", reason)
	return true
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

// SetBrowserContext sets the task-scoped browser grant used by the next task.
func (a *Actor) SetBrowserContext(grantID string, browserID string) {
	a.taskMu.Lock()
	defer a.taskMu.Unlock()
	a.opts.BrowserGrantID = grantID
	a.opts.BrowserID = browserID
}

// SendTask queues a task for execution. Non-blocking if the channel has capacity.
func (a *Actor) SendTask(task protocol.Task) error {
	select {
	case a.taskCh <- task:
		a.taskMu.Lock()
		a.pendingTaskID = task.TaskID
		a.lastActiveAt = time.Now()
		a.taskMu.Unlock()
		return nil
	default:
		return fmt.Errorf("actor busy — task channel full")
	}
}

// HasTaskID reports whether the actor already owns the task either queued or in-flight.
func (a *Actor) HasTaskID(taskID string) bool {
	a.taskMu.Lock()
	defer a.taskMu.Unlock()
	return taskID != "" && (a.taskID == taskID || a.pendingTaskID == taskID)
}

func (a *Actor) HandleContextStatsRequest(ctx context.Context, request *protocol.ContextStatsRequest) {
	a.handleContextStatsRequest(ctx, request)
}

func (a *Actor) HandleCompactRequest(ctx context.Context, request *protocol.CompactRequest) {
	a.handleCompactRequest(ctx, request)
}

func (a *Actor) effectiveInteractionTimeout() time.Duration {
	if a.interactionTimeout > 0 {
		return a.interactionTimeout
	}
	return defaultInteractionTimeout
}

func (a *Actor) currentTime() time.Time {
	if a.now == nil {
		return time.Now()
	}
	return a.now()
}

func (a *Actor) handleContextStatsRequest(ctx context.Context, request *protocol.ContextStatsRequest) {
	result, err := a.runPiControl(ctx, pi.ControlCommand{Type: pi.ControlCommandGetSessionStats}, nil)
	if err != nil {
		slog.Warn("Pi context stats request failed",
			"session", request.SessionID,
			"channel", request.ChannelID,
			"request", request.RequestID,
			"error", err,
		)
		sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		_ = a.opts.Relay.Send(sendCtx, &protocol.ContextStats{
			Type:                 protocol.MsgTypeContextStats,
			SessionID:            request.SessionID,
			ChannelID:            request.ChannelID,
			RequestID:            request.RequestID,
			ContextWindow:        0,
			ReserveTokens:        piReserveTokens,
			KeepRecentTokens:     piKeepRecentTokens,
			AutoThresholdPercent: 0,
			Source:               "pi",
			ObservedAt:           a.currentTime().UTC(),
		})
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_ = a.opts.Relay.Send(sendCtx, contextStatsFromPi(request.SessionID, request.ChannelID, request.RequestID, result.ContextUsage, a.currentTime().UTC()))
}

func contextStatsFromPi(sessionID string, channelID string, requestID string, usage *pi.ContextUsage, observedAt time.Time) *protocol.ContextStats {
	contextWindow := int64(0)
	var tokens *int64
	var percent *float64
	if usage != nil {
		contextWindow = usage.ContextWindow
		tokens = usage.Tokens
		percent = usage.Percent
	}
	return &protocol.ContextStats{
		Type:                 protocol.MsgTypeContextStats,
		SessionID:            sessionID,
		ChannelID:            channelID,
		RequestID:            requestID,
		Tokens:               tokens,
		ContextWindow:        contextWindow,
		Percent:              percent,
		ReserveTokens:        piReserveTokens,
		KeepRecentTokens:     piKeepRecentTokens,
		AutoThresholdPercent: pi.AutoThresholdPercent(contextWindow),
		Source:               "pi",
		ObservedAt:           observedAt,
	}
}

func (a *Actor) handleCompactRequest(ctx context.Context, request *protocol.CompactRequest) {
	var completed *protocol.CompactStatus
	_, err := a.runPiControl(ctx, pi.ControlCommand{
		Type:               pi.ControlCommandCompact,
		CustomInstructions: request.Instructions,
	}, func(event pi.ControlEvent) {
		status := compactStatusFromPiEvent(request, event, a.currentTime().UTC())
		if status.Status == protocol.CompactStatusCompleted {
			completed = status
			return
		}
		sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		_ = a.opts.Relay.Send(sendCtx, status)
	})
	if err != nil {
		sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		_ = a.opts.Relay.Send(sendCtx, &protocol.CompactStatus{
			Type:                 protocol.MsgTypeCompactStatus,
			SessionID:            request.SessionID,
			ChannelID:            request.ChannelID,
			RequestID:            request.RequestID,
			Status:               protocol.CompactStatusFailed,
			Reason:               protocol.CompactReasonManual,
			Instructions:         request.Instructions,
			ContextWindow:        0,
			ReserveTokens:        piReserveTokens,
			KeepRecentTokens:     piKeepRecentTokens,
			AutoThresholdPercent: 0,
			Error:                err.Error(),
			Source:               "pi",
			ObservedAt:           a.currentTime().UTC(),
		})
		return
	}
	if completed != nil {
		sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		_ = a.opts.Relay.Send(sendCtx, completed)
	}
}

func compactStatusFromPiEvent(request *protocol.CompactRequest, event pi.ControlEvent, observedAt time.Time) *protocol.CompactStatus {
	status := protocol.CompactStatusCompleted
	var tokensBefore *int64
	var tokensAfter *int64
	contextWindow := int64(0)
	switch event.Type {
	case pi.ControlEventCompactionStart:
		status = protocol.CompactStatusStarted
	case pi.ControlEventCompactionEnd:
		status = protocol.CompactStatusCompleted
	}
	if event.ContextUsage != nil {
		contextWindow = event.ContextUsage.ContextWindow
		switch event.Type {
		case pi.ControlEventCompactionStart:
			tokensBefore = event.ContextUsage.Tokens
		case pi.ControlEventCompactionEnd:
			tokensAfter = event.ContextUsage.Tokens
		}
	}

	return &protocol.CompactStatus{
		Type:                 protocol.MsgTypeCompactStatus,
		SessionID:            request.SessionID,
		ChannelID:            request.ChannelID,
		RequestID:            request.RequestID,
		Status:               status,
		Reason:               pi.NormalizeCompactReason(event.Reason),
		Instructions:         request.Instructions,
		TokensBefore:         tokensBefore,
		TokensAfter:          tokensAfter,
		ContextWindow:        contextWindow,
		ReserveTokens:        piReserveTokens,
		KeepRecentTokens:     piKeepRecentTokens,
		AutoThresholdPercent: pi.AutoThresholdPercent(contextWindow),
		Summary:              event.Summary,
		FirstKeptEntryID:     event.FirstKeptEntryID,
		Source:               "pi",
		ObservedAt:           observedAt,
	}
}

// pendingFileToolSweepInterval is how often the actor scans pendingFileToolStarts
// for stale entries. pendingFileToolMaxAge is the TTL beyond which an unmatched
// start event is dropped. They are independent dials that happen to share a
// value: pi tool executions are sub-second, so 60 seconds is a generous safety
// margin against missing-end events (pi crash, abandoned tool call), not a
// real timeout for any normal write/edit.
const (
	pendingFileToolSweepInterval = 60 * time.Second
	pendingFileToolMaxAge        = 60 * time.Second
)

// Run is the actor's main loop. It waits for tasks, spawns executors, and
// handles permission flows. Blocks until ctx is canceled or Stop is called.
func (a *Actor) Run(ctx context.Context) error {
	sweepTicker := time.NewTicker(pendingFileToolSweepInterval)
	defer sweepTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-a.stopCh:
			return nil
		case <-sweepTicker.C:
			a.sweepStalePendingFileTools(pendingFileToolMaxAge)
		case task := <-a.taskCh:
			if err := a.executeTask(ctx, task); err != nil {
				slog.Error("task failed", "taskId", task.TaskID, "sessionId", a.opts.SessionID, "err", err)
				sendCtx, sendCancel := context.WithTimeout(ctx, 30*time.Second)
				_ = a.opts.Relay.Send(sendCtx, &protocol.TaskError{
					Type:        protocol.MsgTypeTaskError,
					TaskID:      task.TaskID,
					SessionID:   a.opts.SessionID,
					ChannelID:   task.ChannelID,
					Error:       err.Error(),
					RequestID:   task.RequestID,
					Traceparent: task.Traceparent,
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
	a.pendingTaskID = ""
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
		if a.opts.OnTaskIdle != nil {
			a.opts.OnTaskIdle()
		}
	}()

	contextBlock := daemonfs.BuildContextRefBlock(task.CWD, task.ContextRefs, daemonfs.DefaultContextRefLimits())
	executionPrompt := contextBlock + task.Prompt
	tc := &taskContext{
		TaskID:             task.TaskID,
		ChannelID:          task.ChannelID,
		ActorContext:       ctx,
		StartedAt:          time.Now(),
		OriginalPrompt:     task.Prompt,
		ExecutionPrompt:    executionPrompt,
		Engine:             task.Engine,
		Provider:           task.Provider,
		Model:              task.Model,
		Effort:             task.Effort,
		PermissionMode:     task.PermissionMode,
		RequestID:          task.RequestID,
		Traceparent:        task.Traceparent,
		ImageURLs:          task.ImageURLs,
		ContextRefs:        task.ContextRefs,
		CustomInstructions: task.CustomInstructions,
		DisableSkills:      task.DisableSkills,
		BrowserGrantID:     a.opts.BrowserGrantID,
		BrowserID:          a.opts.BrowserID,
		PlanCapability:     task.PlanCapability,
	}

	logAttrs := []any{"task", task.TaskID, "session", a.opts.SessionID, "promptLen", len(task.Prompt)}
	if task.RequestID != "" {
		logAttrs = append(logAttrs, "requestId", task.RequestID)
	}
	if task.Traceparent != "" {
		logAttrs = append(logAttrs, "traceId", protocol.TraceID(task.Traceparent))
	}
	slog.Info("task received", logAttrs...)
	slog.Debug("task prompt", "task", task.TaskID, "prompt", truncate(task.Prompt, 200))

	sendCtx, sendCancel := context.WithTimeout(ctx, 30*time.Second)
	if err := a.opts.Relay.Send(sendCtx, &protocol.TaskStarted{
		Type:        protocol.MsgTypeTaskStarted,
		TaskID:      task.TaskID,
		SessionID:   a.opts.SessionID,
		ChannelID:   tc.ChannelID,
		StartedAt:   tc.StartedAt.UTC().Format(time.RFC3339Nano),
		RequestID:   tc.RequestID,
		Traceparent: tc.Traceparent,
	}); err != nil {
		sendCancel()
		return fmt.Errorf("send taskStarted: %w", err)
	}
	sendCancel()

	err := a.runExecutor(ctx, taskCtx, tc, tc.ExecutionPrompt)

	// If the task context was cancelled (user hit ESC or timeout), send the
	// appropriate message and loop back for the next task.
	if taskCtx.Err() != nil && ctx.Err() == nil {
		if taskCtx.Err() == context.DeadlineExceeded {
			errCtx, errCancel := context.WithTimeout(ctx, 30*time.Second)
			_ = a.opts.Relay.Send(errCtx, &protocol.TaskError{
				Type:        protocol.MsgTypeTaskError,
				TaskID:      task.TaskID,
				SessionID:   a.opts.SessionID,
				ChannelID:   tc.ChannelID,
				Error:       fmt.Sprintf("task timed out after %s", a.taskTimeout),
				RequestID:   task.RequestID,
				Traceparent: tc.Traceparent,
			})
			errCancel()
			return nil
		}
		cancelCtx, cancelCancel := context.WithTimeout(ctx, 30*time.Second)
		_ = a.opts.Relay.Send(cancelCtx, &protocol.TaskCancelled{
			Type:        protocol.MsgTypeTaskCancelled,
			TaskID:      task.TaskID,
			SessionID:   a.opts.SessionID,
			ChannelID:   tc.ChannelID,
			RequestID:   task.RequestID,
			Traceparent: tc.Traceparent,
		})
		cancelCancel()
		return nil
	}

	return err
}

type executorRunner func(context.Context, func(claude.Event) error) error

func taskActorContext(tc *taskContext, fallback context.Context) context.Context {
	if tc.ActorContext != nil {
		return tc.ActorContext
	}
	return fallback
}

func (a *Actor) runExecutor(actorCtx context.Context, taskCtx context.Context, tc *taskContext, prompt string) error {
	switch tc.Engine {
	case "", "pi":
		return a.runPiExecutor(actorCtx, taskCtx, tc, prompt)
	default:
		return fmt.Errorf("unsupported task engine %q", tc.Engine)
	}
}

func (a *Actor) runPiExecutor(actorCtx context.Context, taskCtx context.Context, tc *taskContext, prompt string) error {
	model := tc.Model
	if model == "" {
		model = a.opts.Model
	}
	model = normalizePiModel(model)
	a.setPiModel(model)
	provider := pi.ProviderOrDefault(tc.Provider)
	binaryPath := a.opts.PiBinaryPath
	if binaryPath == "" {
		binaryPath = "pi"
	}
	sessionFile, err := piSessionFileForSession(a.opts.SessionID)
	if err != nil {
		return err
	}
	skillPrompt := tc.OriginalPrompt
	if skillPrompt == "" {
		skillPrompt = prompt
	}
	skillPaths := []string(nil)
	if !tc.DisableSkills && skills.PromptHasClaudeSkillReference(skillPrompt) {
		availableSkills, err := skills.DiscoverClaudeSkills(a.opts.CWD)
		if err != nil {
			slog.Warn("discover claude skills failed", "cwd", a.opts.CWD, "err", err)
		}
		selectedSkills := skills.SelectClaudeSkillsForPrompt(skillPrompt, availableSkills)
		skillPaths = make([]string, 0, len(selectedSkills))
		for _, skill := range selectedSkills {
			skillPaths = append(skillPaths, skill.Path)
		}
	}

	opts := pi.Options{
		BinaryPath:         binaryPath,
		CWD:                a.opts.CWD,
		Model:              model,
		ResumeSession:      sessionFile,
		TaskID:             tc.TaskID,
		Prompt:             prompt,
		CustomInstructions: tc.CustomInstructions,
		ExtensionPath:      a.opts.PiExtensionPath,
		Provider:           provider,
		SkillPaths:         skillPaths,
		DisableSkills:      tc.DisableSkills,
		BrowserGrantID:     tc.BrowserGrantID,
		BrowserID:          tc.BrowserID,
		BrowserSessionID:   a.opts.SessionID,
		WarmClaudeSDK:      a.opts.WarmClaudeSDK,
		PlanCapability:     tc.PlanCapability,
	}

	coordinator := &structuredQuestionCoordinator{}
	if a.useWarmPiWorker {
		return a.runPiWorker(actorCtx, taskCtx, tc, prompt, opts, coordinator)
	}

	exec := pi.NewExecutor(opts)
	a.attachPiPIDCallbacks(exec, tc.TaskID)
	exec.OnToolExecutionStart = a.capturePiToolStart(coordinator)
	exec.OnToolExecutionEnd = a.capturePiToolEnd(tc.ChannelID)

	resultRaw, err := a.forwardExecutorEvents(actorCtx, taskCtx, tc, func(ctx context.Context, onEvent func(claude.Event) error) error {
		return exec.Run(ctx, onEvent, a.makePiUIHandler(ctx, tc, coordinator))
	})
	if err != nil {
		return err
	}

	return a.handleResult(taskCtx, tc, resultRaw)
}

func (a *Actor) runPiWorker(actorCtx context.Context, taskCtx context.Context, tc *taskContext, prompt string, opts pi.Options, coordinator *structuredQuestionCoordinator) error {
	key := pi.NewWorkerKey(opts)
	a.piWorkerMu.Lock()
	worker := a.piWorker
	if worker != nil && worker.Key() != key {
		a.piWorker = nil
		a.piWorkerMu.Unlock()
		_ = worker.Stop(actorCtx)
		a.piWorkerMu.Lock()
		worker = nil
	}
	if worker == nil {
		worker = pi.NewWorker(opts)
		a.attachPiWorkerPIDCallbacks(worker, tc.TaskID)
		a.piWorker = worker
		a.piWorkerKey = key
	}
	a.piWorkerMu.Unlock()

	resultRaw, err := a.forwardExecutorEvents(actorCtx, taskCtx, tc, func(ctx context.Context, onEvent func(claude.Event) error) error {
		return worker.Prompt(ctx, pi.PromptRequest{
			TaskID:               tc.TaskID,
			Message:              prompt,
			OnEvent:              onEvent,
			OnUIRequest:          a.makePiUIHandler(ctx, tc, coordinator),
			OnToolExecutionStart: a.capturePiToolStart(coordinator),
			OnToolExecutionEnd:   a.capturePiToolEnd(tc.ChannelID),
		})
	})
	if err != nil {
		a.stopPiWorker(actorCtx)
		return err
	}

	return a.handleResult(taskCtx, tc, resultRaw)
}

func (a *Actor) capturePiToolStart(coordinator *structuredQuestionCoordinator) func(pi.ToolExecutionStart) {
	return func(event pi.ToolExecutionStart) {
		if toolName, ok := normalizePiFileToolName(event.ToolName); ok {
			a.pendingFileToolStarts.Store(event.ToolCallID, pendingFileTool{
				toolName:  toolName,
				args:      event.Args,
				startedAt: time.Now(),
			})
		}

		if event.ToolName != "ask_user_questions" {
			return
		}
		round, err := parseStructuredQuestionRound(event.ToolCallID, event.Args)
		if err != nil {
			slog.Warn("invalid structured ask_user_questions payload", "toolCallID", event.ToolCallID, "err", err)
			return
		}
		coordinator.put(round)
	}
}

// capturePiToolEnd returns a callback that emits a file_activity Stream event
// when pi successfully writes or edits a file. Errored invocations and other
// tool names are ignored. The pending start entry is removed once consumed.
func (a *Actor) capturePiToolEnd(channelID string) func(pi.ToolExecutionEnd) {
	return func(event pi.ToolExecutionEnd) {
		toolName, ok := normalizePiFileToolName(event.ToolName)
		if !ok {
			return
		}
		v, ok := a.pendingFileToolStarts.LoadAndDelete(event.ToolCallID)
		if !ok {
			return
		}
		if event.IsError {
			return
		}
		started, ok := v.(pendingFileTool)
		if !ok {
			return
		}

		path := fileToolPath(started.args)
		if path == "" {
			return
		}
		if started.toolName != "" {
			toolName = started.toolName
		}
		if a.opts.RecordTouchedFile != nil {
			a.opts.RecordTouchedFile(channelID, a.opts.CWD, path)
		}
		if toolName == "read" {
			return
		}

		firstChangedLine := 0
		if d, ok := event.Result["details"].(map[string]any); ok {
			if n, ok := d["firstChangedLine"].(float64); ok {
				firstChangedLine = int(n)
			}
		}

		payload, err := json.Marshal(map[string]any{
			"type":             "file_activity",
			"op":               toolName,
			"path":             path,
			"cwd":              a.opts.CWD,
			"firstChangedLine": firstChangedLine,
			"ts":               time.Now().UnixMilli(),
			"toolCallId":       event.ToolCallID,
		})
		if err != nil {
			slog.Warn("file_activity marshal failed", "toolCallID", event.ToolCallID, "err", err)
			return
		}

		next := atomic.AddInt64(&a.seq, 1)
		frame := &protocol.Stream{
			Type:           protocol.MsgTypeStream,
			SessionID:      a.opts.SessionID,
			ChannelID:      channelID,
			SequenceNumber: next,
			Event:          json.RawMessage(payload),
		}
		sendCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.opts.Relay.Send(sendCtx, frame); err != nil {
			slog.Warn("file_activity send failed", "toolCallID", event.ToolCallID, "err", err)
		}
	}
}

func normalizePiFileToolName(name string) (string, bool) {
	switch strings.ToLower(name) {
	case "read", "write", "edit":
		return strings.ToLower(name), true
	default:
		return "", false
	}
}

func fileToolPath(args map[string]any) string {
	if path, ok := args["path"].(string); ok {
		return path
	}
	if path, ok := args["file_path"].(string); ok {
		return path
	}
	return ""
}

// sweepStalePendingFileTools removes pendingFileToolStarts entries older than
// maxAge. Called periodically to bound memory in case pi sends a file tool
// start event with no matching end (crash, abandoned tool call, etc.).
func (a *Actor) sweepStalePendingFileTools(maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	a.pendingFileToolStarts.Range(func(k, v any) bool {
		if v.(pendingFileTool).startedAt.Before(cutoff) {
			a.pendingFileToolStarts.Delete(k)
		}
		return true
	})
}

func normalizePiModel(model string) string {
	model = strings.TrimSpace(model)
	if i := strings.LastIndex(model, "["); i > 0 && strings.HasSuffix(model, "]") {
		suffix := model[i+1 : len(model)-1]
		hasDigit := false
		for _, r := range suffix {
			if r >= '0' && r <= '9' {
				hasDigit = true
				continue
			}
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				continue
			}
			return model
		}
		if hasDigit {
			return strings.TrimSpace(model[:i])
		}
	}
	return model
}

func (a *Actor) setPiModel(model string) {
	if model == "" {
		return
	}
	a.taskMu.Lock()
	a.piModel = model
	a.taskMu.Unlock()
}

func (a *Actor) currentPiModel() string {
	a.taskMu.Lock()
	defer a.taskMu.Unlock()
	if a.piModel != "" {
		return a.piModel
	}
	return normalizePiModel(a.opts.Model)
}

func (a *Actor) attachPiPIDCallbacks(exec *pi.Executor, taskID string) {
	if a.pidDir == "" {
		return
	}
	exec.OnPIDStart = func(pid int) {
		path := filepath.Join(a.pidDir, fmt.Sprintf("%s.pid", taskID))
		if err := pidfile.Write(path, pid); err != nil {
			slog.Warn("write pid file failed", "taskId", taskID, "path", path, "err", err)
		}
	}
	exec.OnPIDExit = func(pid int) {
		path := filepath.Join(a.pidDir, fmt.Sprintf("%s.pid", taskID))
		pidfile.Remove(path)
	}
}

func (a *Actor) attachPiWorkerPIDCallbacks(worker *pi.Worker, taskID string) {
	if a.pidDir == "" {
		return
	}
	worker.OnPIDStart = func(pid int) {
		path := filepath.Join(a.pidDir, fmt.Sprintf("%s.pid", taskID))
		if err := pidfile.Write(path, pid); err != nil {
			slog.Warn("write pid file failed", "taskId", taskID, "path", path, "err", err)
		}
	}
	worker.OnPIDExit = func(pid int) {
		path := filepath.Join(a.pidDir, fmt.Sprintf("%s.pid", taskID))
		pidfile.Remove(path)
	}
}

func (a *Actor) makePiUIHandler(ctx context.Context, tc *taskContext, coordinator *structuredQuestionCoordinator) pi.UIRequestHandler {
	return func(handlerCtx context.Context, req pi.UIRequest) (string, error) {
		if strings.HasPrefix(req.Title, "Structured question round ready") {
			waitCtx, cancel := context.WithTimeout(handlerCtx, 2*time.Second)
			defer cancel()
			if round, ok := coordinator.wait(waitCtx); ok {
				return a.handleStructuredQuestionRound(ctx, handlerCtx, tc, round)
			}
			if round, ok := parseStructuredQuestionRoundFromPlaceholder(req.ID, req.Placeholder); ok {
				coordinator.discardNextMatching(round)
				return a.handleStructuredQuestionRound(ctx, handlerCtx, tc, round)
			}
		}

		question := req.Title
		if question == "" {
			question = "The agent is asking for input."
		}

		ch := make(chan string, 1)
		a.questionMu.Lock()
		a.questionCh[req.ID] = ch
		a.questionMu.Unlock()

		defer func() {
			a.questionMu.Lock()
			delete(a.questionCh, req.ID)
			a.questionMu.Unlock()
		}()

		sendCtx, sendCancel := context.WithTimeout(ctx, 30*time.Second)
		if err := a.opts.Relay.Send(sendCtx, &protocol.Question{
			Type:      protocol.MsgTypeQuestion,
			SessionID: a.opts.SessionID,
			ChannelID: tc.ChannelID,
			RequestID: req.ID,
			Question:  question,
		}); err != nil {
			sendCancel()
			return "", err
		}
		sendCancel()

		timeout := time.NewTimer(a.effectiveInteractionTimeout())
		defer timeout.Stop()

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-handlerCtx.Done():
			return "", handlerCtx.Err()
		case <-a.stopCh:
			return "", fmt.Errorf("actor stopped while waiting for pi UI response")
		case <-timeout.C:
			return "", fmt.Errorf("timed out waiting for question response after %s", a.effectiveInteractionTimeout())
		case answer := <-ch:
			return answer, nil
		}
	}
}

func (a *Actor) handleStructuredQuestionRound(ctx context.Context, handlerCtx context.Context, tc *taskContext, round structuredQuestionRound) (string, error) {
	questions := round.toProtocolQuestions()
	answers := make(map[string]string, len(questions))
	channels := make(map[string]chan string, len(questions))
	timeout := time.NewTimer(a.effectiveInteractionTimeout())
	defer timeout.Stop()

	a.questionMu.Lock()
	for _, question := range questions {
		ch := make(chan string, 1)
		channels[question.RequestID] = ch
		a.questionCh[question.RequestID] = ch
	}
	a.questionMu.Unlock()

	defer func() {
		a.questionMu.Lock()
		defer a.questionMu.Unlock()
		for _, question := range questions {
			delete(a.questionCh, question.RequestID)
		}
	}()

	for _, question := range questions {
		sendCtx, sendCancel := context.WithTimeout(ctx, 30*time.Second)
		if err := a.opts.Relay.Send(sendCtx, &protocol.Question{
			Type:        protocol.MsgTypeQuestion,
			SessionID:   a.opts.SessionID,
			ChannelID:   tc.ChannelID,
			RequestID:   question.RequestID,
			Question:    question.Question,
			Header:      question.Header,
			MultiSelect: question.MultiSelect,
			Options:     question.Options,
		}); err != nil {
			sendCancel()
			return "", err
		}
		sendCancel()
	}

	for _, question := range questions {
		select {
		case answer := <-channels[question.RequestID]:
			answers[question.RequestID] = answer
		case <-ctx.Done():
			return "", ctx.Err()
		case <-handlerCtx.Done():
			return "", handlerCtx.Err()
		case <-a.stopCh:
			return "", fmt.Errorf("actor stopped while waiting for structured question response")
		case <-timeout.C:
			return "", fmt.Errorf("timed out waiting for structured question response after %s", a.effectiveInteractionTimeout())
		}
	}
	return formatStructuredQuestionResponse(round, answers), nil
}

func (a *Actor) forwardExecutorEvents(actorCtx context.Context, taskCtx context.Context, tc *taskContext, run executorRunner) (json.RawMessage, error) {
	var resultRaw json.RawMessage
	const maxConsecutiveFailures = 3
	consecutiveFailures := 0
	localServers := newLocalServerDetector()

	err := run(taskCtx, func(e claude.Event) error {
		next := atomic.AddInt64(&a.seq, 1)

		// Send to relay
		frame := &protocol.Stream{
			Type:           protocol.MsgTypeStream,
			SessionID:      a.opts.SessionID,
			ChannelID:      tc.ChannelID,
			SequenceNumber: next,
			Event:          e.Raw,
		}
		sendCtx, sendCancel := context.WithTimeout(taskCtx, 5*time.Second)
		if err := a.opts.Relay.Send(sendCtx, frame); err != nil {
			consecutiveFailures++
			slog.Warn("relay send failed",
				"sessionId", a.opts.SessionID,
				"seq", next,
				"consecutiveFailures", consecutiveFailures,
				"maxConsecutiveFailures", maxConsecutiveFailures,
				"err", err,
			)
			if consecutiveFailures >= maxConsecutiveFailures {
				sendCancel()
				return fmt.Errorf("relay unreachable: %d consecutive send failures", consecutiveFailures)
			}
		} else {
			consecutiveFailures = 0
		}
		sendCancel()

		if e.Type == "result" {
			resultRaw = make([]byte, len(e.Raw))
			copy(resultRaw, e.Raw)
		}

		// Detect image reads and upload asynchronously.
		if a.opts.Uploader != nil && e.Type == "assistant" {
			slog.Debug("checking assistant event for image reads", "session", a.opts.SessionID)
			a.maybeUploadImages(taskCtx, e.Raw, tc.ChannelID, next)
		}
		a.maybeReportLocalServers(actorCtx, tc, localServers, e.Raw)

		return nil
	})

	if err != nil {
		return nil, err
	}

	if resultRaw == nil {
		return nil, fmt.Errorf("executor exited without result event")
	}

	return resultRaw, nil
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
	if err := json.Unmarshal(raw, &payload); err != nil {
		slog.Error("failed to parse final result payload",
			"session", a.opts.SessionID,
			"taskId", tc.TaskID,
			"channelId", tc.ChannelID,
			"requestId", tc.RequestID,
			"err", err,
			"raw", truncate(string(raw), 512),
		)
		return fmt.Errorf("parse final result payload: %w", err)
	}

	resultSessionID := payload.SessionID
	if tc.Engine == "" || tc.Engine == "pi" {
		resultSessionID = ""
	}

	if resultSessionID != "" {
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
		ClaudeSessionID: resultSessionID,
		InputTokens: int64(
			payload.Usage.InputTokens +
				payload.Usage.CacheReadInput +
				payload.Usage.CacheCreationInput,
		),
		OutputTokens: int64(payload.Usage.OutputTokens),
		CostUSD:      cost,
		DurationMs:   payload.DurationMs,
		RequestID:    tc.RequestID,
		Traceparent:  tc.Traceparent,
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
		slog.Info("permission request sent", "tool", denial.ToolName)
		slog.Debug("permission request detail", "tool", denial.ToolName, "summary", summary)

		// Wait for response
		timeout := time.NewTimer(a.effectiveInteractionTimeout())
		select {
		case <-ctx.Done():
			timeout.Stop()
			return ctx.Err()
		case <-a.stopCh:
			timeout.Stop()
			return fmt.Errorf("actor stopped while waiting for permission")
		case <-timeout.C:
			return fmt.Errorf("timed out waiting for permission response after %s", a.effectiveInteractionTimeout())
		case resp := <-a.permCh:
			timeout.Stop()
			if resp.Approved {
				slog.Info("permission approved, resuming", "tool", denial.ToolName)
				a.addAllowedTool(denial.ToolName)
				return a.runExecutor(taskActorContext(tc, ctx), ctx, tc, tc.ExecutionPrompt)
			}
			return a.handleDenyResponse(ctx, tc)
		}
	}
	return nil
}

// parsedQuestion holds the extracted fields from a single AskUserQuestion denial.
type parsedOption struct {
	Label       string
	Description string
	Preview     string
}

type parsedQuestion struct {
	RequestID   string
	Text        string
	Header      string
	MultiSelect bool
	Options     []parsedOption
}

// parseQuestionDenial extracts questions from an AskUserQuestion tool_input.
// Claude may pack multiple questions into a single tool call's questions array.
func parseQuestionDenial(requestID string, toolInput json.RawMessage) []parsedQuestion {
	var qPayload struct {
		Questions []struct {
			Question    string `json:"question"`
			Header      string `json:"header"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
				Preview     string `json:"preview"`
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
		var opts []parsedOption
		for _, o := range q.Options {
			opts = append(opts, parsedOption{
				Label:       o.Label,
				Description: o.Description,
				Preview:     o.Preview,
			})
		}
		return []parsedQuestion{{
			RequestID:   requestID,
			Text:        q.Question,
			Header:      q.Header,
			MultiSelect: q.MultiSelect,
			Options:     opts,
		}}
	}

	// Multiple questions in one denial — synthesize sub-requestIDs.
	var out []parsedQuestion
	for i, q := range qPayload.Questions {
		var opts []parsedOption
		for _, o := range q.Options {
			opts = append(opts, parsedOption{
				Label:       o.Label,
				Description: o.Description,
				Preview:     o.Preview,
			})
		}
		out = append(out, parsedQuestion{
			RequestID:   fmt.Sprintf("%s_%d", requestID, i),
			Text:        q.Question,
			Header:      q.Header,
			MultiSelect: q.MultiSelect,
			Options:     opts,
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
		options := make([]protocol.QuestionOption, 0, len(q.Options))
		for _, opt := range q.Options {
			options = append(options, protocol.QuestionOption{
				Label:       opt.Label,
				Description: opt.Description,
				Preview:     opt.Preview,
			})
		}
		sendCtx, sendCancel := context.WithTimeout(ctx, 30*time.Second)
		if err := a.opts.Relay.Send(sendCtx, &protocol.Question{
			Type:        protocol.MsgTypeQuestion,
			SessionID:   a.opts.SessionID,
			ChannelID:   tc.ChannelID,
			RequestID:   q.RequestID,
			Question:    q.Text,
			Header:      q.Header,
			MultiSelect: q.MultiSelect,
			Options:     options,
		}); err != nil {
			sendCancel()
			return err
		}
		sendCancel()
		slog.Info("question sent", "requestID", q.RequestID, "questionLen", len(q.Text))
		slog.Debug("question detail", "requestID", q.RequestID, "question", q.Text)
	}

	slog.Debug("waiting for answers", "count", len(allQuestions))

	// Collect all answers. Order doesn't matter — each channel is keyed by requestID.
	answers := make(map[string]string, len(allQuestions))
	timeout := time.NewTimer(a.effectiveInteractionTimeout())
	defer timeout.Stop()
	for _, q := range allQuestions {
		ch := a.questionCh[q.RequestID]
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-a.stopCh:
			return fmt.Errorf("actor stopped while waiting for answers")
		case <-timeout.C:
			return fmt.Errorf("timed out waiting for question response after %s", a.effectiveInteractionTimeout())
		case answer := <-ch:
			answers[q.RequestID] = answer
			slog.Info("answer received", "requestID", q.RequestID, "answerLen", len(answer))
			slog.Debug("answer detail", "requestID", q.RequestID, "answer", answer)
		}
	}

	// Compose a single prompt with all answers.
	var parts []string
	for _, q := range allQuestions {
		parts = append(parts, fmt.Sprintf("Q: %s\nA: %s", q.Text, answers[q.RequestID]))
	}
	answerPrompt := "My answers:\n\n" + strings.Join(parts, "\n\n")

	return a.runExecutor(taskActorContext(tc, ctx), ctx, tc, answerPrompt)
}

// maybeUploadImages inspects an "assistant" event for Read tool_use blocks
// targeting image files. For each image found, it reads the file from disk and
// uploads it to the relay in a background goroutine, then sends a supplementary
// image_url stream event. Best-effort: failures are logged, not propagated.
func (a *Actor) maybeUploadImages(taskCtx context.Context, raw json.RawMessage, channelID string, afterSeq int64) {
	var evt struct {
		Type    string `json:"type"`
		Message struct {
			Content []struct {
				Type  string `json:"type"`
				Name  string `json:"name"`
				ID    string `json:"id"`
				Input struct {
					FilePath string `json:"file_path"`
				} `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &evt) != nil {
		return
	}

	slog.Debug("maybeUploadImages", "eventType", evt.Type, "contentBlocks", len(evt.Message.Content))
	for _, block := range evt.Message.Content {
		slog.Debug("inspecting content block", "type", block.Type, "name", block.Name, "filePath", block.Input.FilePath)
		if block.Type != "tool_use" || block.Name != "Read" {
			continue
		}
		filePath := block.Input.FilePath
		if filePath == "" || !upload.IsImageFile(filePath) {
			slog.Debug("skipping non-image read", "filePath", filePath, "isImage", upload.IsImageFile(filePath))
			continue
		}
		toolUseID := block.ID
		slog.Info("image read detected, uploading", "filePath", filePath, "toolUseId", toolUseID)

		// Resolve relative paths against actor CWD.
		absPath := filePath
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(a.opts.CWD, absPath)
		}

		go func(ctx context.Context) {
			if err := ctx.Err(); err != nil {
				return
			}
			data, err := os.ReadFile(absPath)
			if err != nil {
				slog.Warn("image upload: read file failed", "path", absPath, "err", err)
				return
			}

			filename := filepath.Base(absPath)
			imageURL, err := a.opts.Uploader.Upload(ctx, filename, data)
			if err != nil {
				slog.Warn("image upload: upload failed", "path", absPath, "err", err)
				return
			}

			// Send supplementary image_url event to relay.
			next := atomic.AddInt64(&a.seq, 1)
			imgFrame := &protocol.Stream{
				Type:           protocol.MsgTypeStream,
				SessionID:      a.opts.SessionID,
				ChannelID:      channelID,
				SequenceNumber: next,
				Event: json.RawMessage(fmt.Sprintf(
					`{"type":"image_url","toolUseId":%q,"imageUrl":%q}`,
					toolUseID, imageURL,
				)),
			}
			sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if err := a.opts.Relay.Send(sendCtx, imgFrame); err != nil {
				slog.Warn("image upload: send image_url event failed", "err", err)
			} else {
				slog.Info("image uploaded", "path", absPath, "url", imageURL, "toolUseId", toolUseID)
			}
		}(taskCtx)
	}
}

func (a *Actor) handleDenyResponse(ctx context.Context, tc *taskContext) error {
	denyPrompt := "The previous tool request was denied. Please continue without using that tool."
	return a.runExecutor(taskActorContext(tc, ctx), ctx, tc, denyPrompt)
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
	a.stopPiWorker(context.Background())
	select {
	case <-a.stopCh:
	default:
		close(a.stopCh)
	}
	return nil
}

// CancelTask cancels the in-flight task (if any) without shutting down the actor.
// The executor's context.Done fires, stopping the subprocess.
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
