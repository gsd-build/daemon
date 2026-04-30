package session

import (
	"context"
	"testing"
	"time"
)

func TestTurnSupervisorNoFirstEventTimeoutIsRetryable(t *testing.T) {
	sink := &recordingLifecycleSink{}
	supervisor := NewTurnSupervisor(TurnSupervisorOptions{
		TaskID:    "task-1",
		SessionID: "session-1",
		AttemptID: "attempt-1",
		Deadlines: TurnDeadlines{
			FirstEvent:  10 * time.Millisecond,
			CleanupTerm: 10 * time.Millisecond,
		},
		Sink: sink,
	})

	err := supervisor.Run(context.Background(), func(ctx context.Context, hooks TurnHooks) error {
		hooks.PromptWritten()
		<-ctx.Done()
		return ctx.Err()
	})

	result := supervisor.Result()
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if result.FailureCode != "no_first_event_timeout" || !result.Retryable {
		t.Fatalf("result = %#v", result)
	}
	if !sink.HasPhase("prompt_written") || !sink.HasPhase("task_timed_out") {
		t.Fatalf("phases = %#v", sink.Phases())
	}
}

func TestTurnSupervisorToolStartMakesRetryUnsafe(t *testing.T) {
	sink := &recordingLifecycleSink{}
	supervisor := NewTurnSupervisor(TurnSupervisorOptions{
		TaskID:    "task-1",
		SessionID: "session-1",
		AttemptID: "attempt-1",
		Deadlines: TurnDeadlines{
			ToolIdle:    10 * time.Millisecond,
			CleanupTerm: 10 * time.Millisecond,
		},
		Sink: sink,
	})

	_ = supervisor.Run(context.Background(), func(ctx context.Context, hooks TurnHooks) error {
		hooks.PromptWritten()
		hooks.FirstEventSeen()
		hooks.FirstVisibleEventSeen()
		hooks.ToolStarted("tool-1", "bash")
		<-ctx.Done()
		return ctx.Err()
	})

	result := supervisor.Result()
	if result.FailureCode != "tool_idle_timeout" || result.Retryable {
		t.Fatalf("result = %#v", result)
	}
}

type recordingLifecycleSink struct {
	phases []string
}

func (s *recordingLifecycleSink) Phase(phase string, fields map[string]any) {
	s.phases = append(s.phases, phase)
}

func (s *recordingLifecycleSink) HasPhase(phase string) bool {
	for _, got := range s.phases {
		if got == phase {
			return true
		}
	}
	return false
}

func (s *recordingLifecycleSink) Phases() []string {
	return append([]string(nil), s.phases...)
}
