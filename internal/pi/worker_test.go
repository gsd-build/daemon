package pi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gsd-build/daemon/internal/claude"
)

func writeWarmFakePi(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "pi")
	script := `#!/bin/sh
count=0
while IFS= read -r line; do
  case "$line" in
    *'"type":"prompt"'*)
      count=$((count + 1))
      printf '%s\n' '{"type":"response","command":"prompt","success":true}'
      printf '%s\n' "{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_delta\",\"contentIndex\":0,\"delta\":\"turn-$count\"},\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"turn-$count\"}],\"api\":\"anthropic-messages\",\"provider\":\"claude-cli\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input\":0,\"output\":0,\"cacheRead\":0,\"cacheWrite\":0,\"totalTokens\":0,\"cost\":{\"total\":0}}}}"
      printf '%s\n' "{\"type\":\"agent_end\",\"messages\":[{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"turn-$count\"}],\"usage\":{\"input\":1,\"output\":1,\"cacheRead\":0,\"cacheWrite\":0,\"cost\":{\"total\":0.001}}}]}"
      ;;
  esac
done
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}
	return path
}

func TestWorkerReusesOnePiProcessForTwoPrompts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	worker := NewWorker(Options{
		BinaryPath:    writeWarmFakePi(t),
		CWD:           t.TempDir(),
		ExtensionPath: filepath.Join(t.TempDir(), "index.ts"),
		Provider:      "claude-cli",
		Model:         "claude-sonnet-4-6",
	})
	if err := os.WriteFile(worker.opts.ExtensionPath, []byte("// fake"), 0600); err != nil {
		t.Fatalf("write extension: %v", err)
	}
	defer worker.Stop(context.Background())

	var first []string
	if err := worker.Prompt(ctx, PromptRequest{
		TaskID:  "task-1",
		Message: "first",
		OnEvent: func(e claude.Event) error {
			if e.Type == "stream_event" {
				first = append(first, string(e.Raw))
			}
			return nil
		},
	}); err != nil {
		t.Fatalf("first prompt: %v", err)
	}
	firstPID := worker.PID()

	var second []string
	if err := worker.Prompt(ctx, PromptRequest{
		TaskID:  "task-2",
		Message: "second",
		OnEvent: func(e claude.Event) error {
			if e.Type == "stream_event" {
				second = append(second, string(e.Raw))
			}
			return nil
		},
	}); err != nil {
		t.Fatalf("second prompt: %v", err)
	}

	if worker.PID() != firstPID {
		t.Fatalf("worker PID changed: first=%d second=%d", firstPID, worker.PID())
	}
	if !strings.Contains(strings.Join(first, "\n"), "turn-1") {
		t.Fatalf("first prompt did not stream turn-1: %#v", first)
	}
	if !strings.Contains(strings.Join(second, "\n"), "turn-2") {
		t.Fatalf("second prompt did not stream turn-2: %#v", second)
	}
}
