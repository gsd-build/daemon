package logging

import "testing"

func TestPromptPreviewCapsAndCollapsesWhitespace(t *testing.T) {
	got := PromptPreview("write the full update spec\nwith all lifecycle diagnostics and tests")
	if got != "write the full update spec with all" {
		t.Fatalf("preview = %q", got)
	}
}

func TestPromptPreviewRedactsSecrets(t *testing.T) {
	got := PromptPreview("deploy token sk-ant-1234567890abcdef and continue")
	if got == "" || got == "deploy token sk-ant-1234567890abcdef" {
		t.Fatalf("secret leaked in preview: %q", got)
	}
	if want := "deploy token [REDACTED_SECRET] and"; got != want {
		t.Fatalf("preview = %q, want %q", got, want)
	}
}

func TestLifecycleEventFields(t *testing.T) {
	ev := NewTaskLifecycleLog(TaskLifecycleLogInput{
		Phase:      "prompt_written",
		TaskID:     "task-1",
		SessionID:  "session-1",
		RequestID:  "request-1",
		TraceID:    "trace-1",
		AttemptID:  "attempt-1",
		TurnKind:   "user",
		ElapsedMs:  700,
		PromptText: "write the plan",
	})
	if ev.Event != "task_lifecycle" || ev.Phase != "prompt_written" {
		t.Fatalf("event = %#v", ev)
	}
	if ev.PromptPreview != "write the plan" {
		t.Fatalf("prompt preview = %q", ev.PromptPreview)
	}
}
