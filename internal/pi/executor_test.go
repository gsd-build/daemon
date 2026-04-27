package pi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gsd-build/daemon/internal/claude"
)

func TestTerminateProcessGroupAndWaitEscalates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process group signals are unix-specific")
	}

	cmd := exec.Command("sh", "-c", "trap '' TERM; sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start command: %v", err)
	}

	start := time.Now()
	err := terminateProcessGroupAndWait(cmd, cmd.Process.Pid, nil, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected killed process error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("terminate took %s", elapsed)
	}
}

func TestPiExitError_DetectsMissingExtensionDeps(t *testing.T) {
	cases := []struct {
		name       string
		stderr     string
		wantHint   bool
		wantSubstr string
	}{
		{
			name:       "Cannot find module signature",
			stderr:     `Failed to load extension: Cannot find module '@anthropic-ai/claude-agent-sdk'`,
			wantHint:   true,
			wantSubstr: "self-heal will run npm ci",
		},
		{
			name:       "Unknown provider claude-cli signature",
			stderr:     `Unknown provider "claude-cli". Use --list-models to see available providers/models.`,
			wantHint:   true,
			wantSubstr: "self-heal will run npm ci",
		},
		{
			name:       "Generic non-zero exit",
			stderr:     "some unrelated error from pi",
			wantHint:   false,
			wantSubstr: "pi exited with code 1",
		},
		{
			name:       "Empty stderr",
			stderr:     "",
			wantHint:   false,
			wantSubstr: "no stderr",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := piExitError(1, c.stderr)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), c.wantSubstr) {
				t.Errorf("err=%q, want substring %q", err.Error(), c.wantSubstr)
			}
		})
	}
}

func TestExecutorRequiresExistingExtension(t *testing.T) {
	exec := NewExecutor(Options{
		BinaryPath:    "definitely-not-a-real-pi-binary",
		CWD:           t.TempDir(),
		ExtensionPath: t.TempDir() + "/missing/index.ts",
		Prompt:        "hello",
	})

	err := exec.Run(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected missing extension error")
	}
	if !strings.Contains(err.Error(), "pi extension not found") {
		t.Fatalf("expected missing extension error, got %v", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
}

func TestExecutorReportsToolExecutionStart(t *testing.T) {
	t.Run("snake case", func(t *testing.T) {
		var got ToolExecutionStart
		exec := Executor{
			OnToolExecutionStart: func(event ToolExecutionStart) {
				got = event
			},
		}

		raw := json.RawMessage(`{"type":"tool_execution_start","tool_call_id":"toolu_123","tool_name":"ask_user_questions","args":{"questions":[{"id":"scope","question":"Pick","options":[{"label":"A"}]}]}}`)
		if err := exec.handlePiEventForTest(context.Background(), raw, func(_ claude.Event) error { return nil }); err != nil {
			t.Fatalf("handlePiEventForTest: %v", err)
		}
		assertToolExecutionStart(t, got)
	})

	t.Run("camel case", func(t *testing.T) {
		var got ToolExecutionStart
		exec := Executor{
			OnToolExecutionStart: func(event ToolExecutionStart) {
				got = event
			},
		}

		raw := json.RawMessage(`{"type":"tool_execution_start","toolCallId":"toolu_123","toolName":"ask_user_questions","args":{"questions":[{"id":"scope","question":"Pick","options":[{"label":"A"}]}]}}`)
		if err := exec.handlePiEventForTest(context.Background(), raw, func(_ claude.Event) error { return nil }); err != nil {
			t.Fatalf("handlePiEventForTest: %v", err)
		}
		assertToolExecutionStart(t, got)
	})
}

func assertToolExecutionStart(t *testing.T, got ToolExecutionStart) {
	t.Helper()
	if got.ToolCallID != "toolu_123" {
		t.Fatalf("ToolCallID = %q, want toolu_123", got.ToolCallID)
	}
	if got.ToolName != "ask_user_questions" {
		t.Fatalf("ToolName = %q, want ask_user_questions", got.ToolName)
	}
	questions, ok := got.Args["questions"].([]any)
	if !ok || len(questions) != 1 {
		t.Fatalf("questions = %#v, want one question", got.Args["questions"])
	}
	question, ok := questions[0].(map[string]any)
	if !ok {
		t.Fatalf("question = %#v, want object", questions[0])
	}
	if question["id"] != "scope" {
		t.Fatalf("question id = %#v, want scope", question["id"])
	}
}
