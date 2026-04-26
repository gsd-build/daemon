//go:build integration

package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	protocol "github.com/gsd-build/protocol-go"
)

func TestActor_PiEngine_FullRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("pi"); err != nil {
		t.Skip("pi not on PATH")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not on PATH")
	}

	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	preflightPiExtensionDependencies(t, repoRoot)
	piExt := filepath.Join(repoRoot, "internal", "pi", "extension", "index.ts")

	relay := newFakeRelay()
	actor, err := NewActor(Options{
		SessionID:       "sess-pi",
		CWD:             t.TempDir(),
		Relay:           relay,
		Model:           "claude-sonnet-4-6",
		PiExtensionPath: piExt,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	defer actor.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- actor.Run(ctx) }()

	prompt := strings.Join([]string{
		"Two paths for multi-provider in a coding-agent harness:",
		"  A) spawn each provider's CLI binary as a subprocess",
		"  B) use the Anthropic-compat HTTP endpoint each provider exposes",
		"",
		"Use the ask_human tool to ask me which path to take.",
		"Then in ONE short sentence (no preamble) state my choice followed by the literal phrase 'DECISION CONFIRMED'.",
	}, "\n")

	const userAnswer = "Approach B (Anthropic-compat HTTP)."

	if err := actor.SendTask(protocol.Task{
		TaskID:    "t1",
		SessionID: "sess-pi",
		ChannelID: "ch1",
		Prompt:    prompt,
		Engine:    "pi",
	}); err != nil {
		t.Fatalf("send task: %v", err)
	}

	deadline := time.Now().Add(60 * time.Second)
	answered := false
	for time.Now().Before(deadline) {
		frames := relay.GetFrames()
		if !answered {
			for _, f := range frames {
				q, ok := f.(*protocol.Question)
				if !ok {
					continue
				}
				t.Logf("got Question requestID=%s text=%q", q.RequestID, truncatePiTest(q.Question, 120))
				if err := actor.HandleQuestionResponse(&protocol.QuestionResponse{
					Type:      protocol.MsgTypeQuestionResponse,
					ChannelID: "ch1",
					SessionID: "sess-pi",
					RequestID: q.RequestID,
					Answer:    userAnswer,
				}); err != nil {
					t.Fatalf("HandleQuestionResponse: %v", err)
				}
				answered = true
				break
			}
		}
		for _, f := range frames {
			if tc, ok := f.(*protocol.TaskComplete); ok {
				t.Logf("got TaskComplete cost=%s input=%d output=%d duration=%dms",
					tc.CostUSD, tc.InputTokens, tc.OutputTokens, tc.DurationMs)
				if !answered {
					t.Fatal("TaskComplete arrived before any Question; agent never asked")
				}
				if tc.OutputTokens == 0 {
					t.Errorf("TaskComplete output tokens were zero")
				}
				if tc.DurationMs == 0 {
					t.Errorf("TaskComplete duration was zero")
				}
				return
			}
			if tc, ok := f.(*protocol.TaskError); ok {
				t.Fatalf("TaskError: %s", tc.Error)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("timed out waiting for TaskComplete; question seen=" + boolStr(answered))
}

func preflightPiExtensionDependencies(t *testing.T, repoRoot string) {
	t.Helper()

	extensionDir := filepath.Join(repoRoot, "internal", "pi", "extension")
	requiredPaths := []string{
		filepath.Join(extensionDir, "node_modules"),
		filepath.Join(extensionDir, "node_modules", "@anthropic-ai", "claude-agent-sdk"),
		filepath.Join(extensionDir, "node_modules", "@mariozechner", "pi-ai"),
		filepath.Join(extensionDir, "node_modules", "@mariozechner", "pi-coding-agent"),
		filepath.Join(extensionDir, "node_modules", "zod"),
		filepath.Join(extensionDir, "node_modules", "@sinclair", "typebox"),
	}
	for _, path := range requiredPaths {
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				t.Skipf("pi extension dependencies not installed; missing %s", path)
			}
			t.Skipf("pi extension dependency preflight failed for %s: %v", path, err)
		}
	}
}

func truncatePiTest(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
