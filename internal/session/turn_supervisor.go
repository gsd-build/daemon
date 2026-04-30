package session

import (
	"context"
	"sync"
	"time"
)

type TurnDeadlines struct {
	ProcessStart      time.Duration
	PromptWrite       time.Duration
	FirstEvent        time.Duration
	FirstVisibleEvent time.Duration
	StreamIdle        time.Duration
	ToolIdle          time.Duration
	UserInput         time.Duration
	CleanupTerm       time.Duration
}

type TurnResult struct {
	FailureCode string
	Retryable   bool
}

type LifecycleSink interface {
	Phase(phase string, fields map[string]any)
}

type TurnSupervisorOptions struct {
	TaskID        string
	SessionID     string
	ChannelID     string
	RequestID     string
	TraceID       string
	AttemptID     string
	AttemptNumber int
	TurnKind      string
	Deadlines     TurnDeadlines
	Sink          LifecycleSink
}

type TurnHooks struct {
	PromptWritten         func()
	FirstEventSeen        func()
	FirstVisibleEventSeen func()
	ToolStarted           func(toolCallID string, toolName string)
	ToolFinished          func(toolCallID string, toolName string)
}

type TurnSupervisor struct {
	opts         TurnSupervisorOptions
	startedAt    time.Time
	mu           sync.Mutex
	retrySafe    bool
	result       TurnResult
	cancel       context.CancelFunc
	firstEvent   bool
	firstVisible bool
}

func NewTurnSupervisor(opts TurnSupervisorOptions) *TurnSupervisor {
	if opts.TurnKind == "" {
		opts.TurnKind = "user"
	}
	return &TurnSupervisor{opts: opts, retrySafe: true}
}

func (s *TurnSupervisor) Run(parent context.Context, run func(context.Context, TurnHooks) error) error {
	s.startedAt = time.Now()
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	defer cancel()
	s.emit("task_started", nil)

	hooks := TurnHooks{
		PromptWritten: func() {
			s.emit("prompt_written", nil)
			if s.opts.Deadlines.FirstEvent > 0 {
				time.AfterFunc(s.opts.Deadlines.FirstEvent, func() {
					s.mu.Lock()
					shouldTimeout := !s.firstEvent && s.result.FailureCode == ""
					s.mu.Unlock()
					if shouldTimeout {
						s.timeout("no_first_event_timeout", true)
					}
				})
			}
		},
		FirstEventSeen: func() {
			s.mu.Lock()
			s.firstEvent = true
			s.mu.Unlock()
			s.emit("first_event_seen", nil)
		},
		FirstVisibleEventSeen: func() {
			s.mu.Lock()
			s.firstVisible = true
			s.retrySafe = false
			s.mu.Unlock()
			s.emit("first_visible_event_seen", nil)
		},
		ToolStarted: func(toolCallID string, toolName string) {
			s.mu.Lock()
			s.retrySafe = false
			s.mu.Unlock()
			s.emit("tool_started", map[string]any{"toolCallId": toolCallID, "toolName": toolName})
			if s.opts.Deadlines.ToolIdle > 0 {
				time.AfterFunc(s.opts.Deadlines.ToolIdle, func() {
					s.timeout("tool_idle_timeout", false)
				})
			}
		},
		ToolFinished: func(toolCallID string, toolName string) {
			s.emit("tool_finished", map[string]any{"toolCallId": toolCallID, "toolName": toolName})
		},
	}

	err := run(ctx, hooks)
	s.mu.Lock()
	hasFailure := s.result.FailureCode != ""
	s.mu.Unlock()
	if hasFailure {
		return turnFailureError{result: s.Result()}
	}
	return err
}

type turnFailureError struct {
	result TurnResult
}

func (e turnFailureError) Error() string {
	return e.result.FailureCode
}

func taskFailureCode(err error) string {
	if failure, ok := err.(turnFailureError); ok {
		return failure.result.FailureCode
	}
	return ""
}

func taskFailureRetryable(err error) bool {
	if failure, ok := err.(turnFailureError); ok {
		return failure.result.Retryable
	}
	return false
}

func (s *TurnSupervisor) Result() TurnResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result
}

func (s *TurnSupervisor) timeout(code string, retryable bool) {
	s.mu.Lock()
	if s.result.FailureCode != "" {
		s.mu.Unlock()
		return
	}
	s.result = TurnResult{FailureCode: code, Retryable: retryable && s.retrySafe}
	cancel := s.cancel
	s.mu.Unlock()
	s.emit("task_timed_out", map[string]any{"failureCode": code, "retryable": retryable})
	if cancel != nil {
		cancel()
	}
}

func (s *TurnSupervisor) emit(phase string, fields map[string]any) {
	if s.opts.Sink == nil {
		return
	}
	if fields == nil {
		fields = map[string]any{}
	}
	fields["taskId"] = s.opts.TaskID
	fields["sessionId"] = s.opts.SessionID
	fields["attemptId"] = s.opts.AttemptID
	fields["turnKind"] = s.opts.TurnKind
	fields["elapsedMs"] = time.Since(s.startedAt).Milliseconds()
	s.opts.Sink.Phase(phase, fields)
}
