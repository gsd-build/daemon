package pi

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFakePi(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pi")
	script := "#!/usr/bin/env bash\nset -euo pipefail\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}
	return path
}

func TestRunControlSendsCompactInstructions(t *testing.T) {
	outDir := t.TempDir()
	stdinPath := filepath.Join(outDir, "stdin.jsonl")
	fakePi := writeFakePi(t, `
cat > "`+stdinPath+`"
printf '%s\n' '{"type":"compaction_start","reason":"manual","contextUsage":{"tokens":8951,"contextWindow":1000000,"percent":0.8951}}'
printf '%s\n' '{"type":"compaction_end","reason":"manual","summary":"Kept the auth state and file paths.","contextUsage":{"tokens":7712,"contextWindow":1000000,"percent":0.7712},"firstKeptEntryId":"entry_42"}'
printf '%s\n' '{"type":"control_result","ok":true,"contextUsage":{"tokens":7712,"contextWindow":1000000,"percent":0.7712}}'
`)

	var events []ControlEvent
	result, err := RunControl(context.Background(), ControlOptions{
		BinaryPath:  fakePi,
		CWD:         outDir,
		SessionFile: filepath.Join(outDir, "session.jsonl"),
		Command: ControlCommand{
			Type:               ControlCommandCompact,
			CustomInstructions: "preserve auth state and exact file paths",
		},
		OnEvent: func(event ControlEvent) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatalf("RunControl returned error: %v", err)
	}

	raw, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("read stdin capture: %v", err)
	}
	if !strings.Contains(string(raw), `"type":"compact"`) {
		t.Fatalf("stdin did not contain compact command: %s", string(raw))
	}
	if !strings.Contains(string(raw), `"customInstructions":"preserve auth state and exact file paths"`) {
		t.Fatalf("stdin did not contain custom instructions: %s", string(raw))
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 compaction events, got %d", len(events))
	}
	if events[0].Type != ControlEventCompactionStart {
		t.Fatalf("first event type = %q", events[0].Type)
	}
	if events[1].Summary != "Kept the auth state and file paths." {
		t.Fatalf("summary = %q", events[1].Summary)
	}
	if result.ContextUsage == nil || result.ContextUsage.Tokens == nil || *result.ContextUsage.Tokens != 7712 {
		t.Fatalf("unexpected context usage: %+v", result.ContextUsage)
	}
}

func TestRunControlSendsContextStatsRequest(t *testing.T) {
	outDir := t.TempDir()
	stdinPath := filepath.Join(outDir, "stdin.jsonl")
	fakePi := writeFakePi(t, `
cat > "`+stdinPath+`"
printf '%s\n' '{"type":"extension_ui_request","id":"request_1","method":"setWidget","widgetKey":"autoresearch"}'
	printf '%s\n' '{"type":"response","command":"get_session_stats","success":true,"data":{"sessionFile":"session.jsonl","sessionId":"session_1","tokens":{"input":185000,"output":3000,"cacheRead":90000,"cacheWrite":0,"total":278000},"contextUsage":{"tokens":860000,"contextWindow":1000000,"percent":86}}}'
`)

	result, err := RunControl(context.Background(), ControlOptions{
		BinaryPath:  fakePi,
		CWD:         outDir,
		SessionFile: filepath.Join(outDir, "session.jsonl"),
		Model:       "claude-opus-4-6",
		Command: ControlCommand{
			Type: ControlCommandGetSessionStats,
		},
	})
	if err != nil {
		t.Fatalf("RunControl returned error: %v", err)
	}

	raw, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("read stdin capture: %v", err)
	}
	if !strings.Contains(string(raw), `"type":"get_session_stats"`) {
		t.Fatalf("stdin did not contain stats command: %s", string(raw))
	}
	if result.ContextUsage == nil || result.ContextUsage.ContextWindow != longContextWindow {
		encoded, _ := json.Marshal(result)
		t.Fatalf("unexpected result: %s", encoded)
	}
	if result.ContextUsage.Tokens == nil || *result.ContextUsage.Tokens != 860000 {
		t.Fatalf("tokens = %+v", result.ContextUsage.Tokens)
	}
	if result.ContextUsage.Percent == nil || math.Abs(*result.ContextUsage.Percent-86) > 0.0001 {
		t.Fatalf("percent = %+v", result.ContextUsage.Percent)
	}
}

func TestRunControlExcludesUnrelatedHostSecrets(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("OPENROUTER_API_KEY", "openrouter-secret")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")

	outDir := t.TempDir()
	envPath := filepath.Join(outDir, "env.txt")
	fakePi := writeFakePi(t, `
env | sort > "`+envPath+`"
printf '%s\n' '{"type":"control_result","ok":true}'
`)

	_, err := RunControl(context.Background(), ControlOptions{
		BinaryPath:  fakePi,
		CWD:         outDir,
		SessionFile: filepath.Join(outDir, "session.jsonl"),
		Provider:    "openrouter",
		Command:     ControlCommand{Type: ControlCommandGetSessionStats},
	})
	if err != nil {
		t.Fatalf("RunControl returned error: %v", err)
	}

	raw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env capture: %v", err)
	}
	got := string(raw)
	if strings.Contains(got, "AWS_SECRET_ACCESS_KEY=") || strings.Contains(got, "ANTHROPIC_API_KEY=") {
		t.Fatalf("control env leaked unrelated secret: %s", got)
	}
	if !strings.Contains(got, "OPENROUTER_API_KEY=openrouter-secret") {
		t.Fatalf("control env missing selected provider credential: %s", got)
	}
}

func TestRunControlDoesNotUseStatsTokenAccountingAsContextUsage(t *testing.T) {
	outDir := t.TempDir()
	fakePi := writeFakePi(t, `
printf '%s\n' '{"type":"response","command":"get_session_stats","success":true,"data":{"sessionFile":"session.jsonl","sessionId":"session_1","tokens":{"input":185000,"output":3000,"cacheRead":90000,"cacheWrite":0,"total":278000}}}'
`)

	result, err := RunControl(context.Background(), ControlOptions{
		BinaryPath:  fakePi,
		CWD:         outDir,
		SessionFile: filepath.Join(outDir, "session.jsonl"),
		Model:       "claude-opus-4-6",
		Command: ControlCommand{
			Type: ControlCommandGetSessionStats,
		},
	})
	if err != nil {
		t.Fatalf("RunControl returned error: %v", err)
	}
	if result.ContextUsage != nil {
		t.Fatalf("context usage = %+v", result.ContextUsage)
	}
}

func TestRunControlParsesNullStatsContextUsage(t *testing.T) {
	outDir := t.TempDir()
	fakePi := writeFakePi(t, `
printf '%s\n' '{"type":"response","command":"get_session_stats","success":true,"data":{"sessionFile":"session.jsonl","sessionId":"session_1","contextUsage":{"tokens":null,"contextWindow":1000000,"percent":null}}}'
`)

	result, err := RunControl(context.Background(), ControlOptions{
		BinaryPath:  fakePi,
		CWD:         outDir,
		SessionFile: filepath.Join(outDir, "session.jsonl"),
		Model:       "claude-opus-4-6",
		Command: ControlCommand{
			Type: ControlCommandGetSessionStats,
		},
	})
	if err != nil {
		t.Fatalf("RunControl returned error: %v", err)
	}
	if result.ContextUsage == nil || result.ContextUsage.ContextWindow != longContextWindow {
		t.Fatalf("context usage = %+v", result.ContextUsage)
	}
	if result.ContextUsage.Tokens != nil {
		t.Fatalf("tokens = %+v", result.ContextUsage.Tokens)
	}
	if result.ContextUsage.Percent != nil {
		t.Fatalf("percent = %+v", result.ContextUsage.Percent)
	}
}

func TestRunControlParsesCompactResponseFrame(t *testing.T) {
	outDir := t.TempDir()
	stdinPath := filepath.Join(outDir, "stdin.jsonl")
	fakePi := writeFakePi(t, `
cat > "`+stdinPath+`"
printf '%s\n' '{"type":"extension_ui_request","id":"request_1","method":"setWidget","widgetKey":"autoresearch"}'
printf '%s\n' '{"type":"response","command":"compact","success":true,"data":{"summary":"Kept the auth state and file paths.","firstKeptEntryId":"entry_42","tokensBefore":8951}}'
`)

	var events []ControlEvent
	result, err := RunControl(context.Background(), ControlOptions{
		BinaryPath:  fakePi,
		CWD:         outDir,
		SessionFile: filepath.Join(outDir, "session.jsonl"),
		Command: ControlCommand{
			Type:               ControlCommandCompact,
			CustomInstructions: "preserve auth state and exact file paths",
		},
		OnEvent: func(event ControlEvent) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatalf("RunControl returned error: %v", err)
	}

	raw, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("read stdin capture: %v", err)
	}
	if !strings.Contains(string(raw), `"type":"compact"`) {
		t.Fatalf("stdin did not contain compact command: %s", string(raw))
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 compaction events, got %d", len(events))
	}
	if events[0].Type != ControlEventCompactionStart {
		t.Fatalf("first event type = %q", events[0].Type)
	}
	if events[0].ContextUsage == nil || events[0].ContextUsage.Tokens == nil || *events[0].ContextUsage.Tokens != 8951 {
		t.Fatalf("start context usage = %+v", events[0].ContextUsage)
	}
	if events[1].Type != ControlEventCompactionEnd {
		t.Fatalf("second event type = %q", events[1].Type)
	}
	if events[1].Summary != "Kept the auth state and file paths." {
		t.Fatalf("summary = %q", events[1].Summary)
	}
	if events[1].FirstKeptEntryID != "entry_42" {
		t.Fatalf("first kept entry = %q", events[1].FirstKeptEntryID)
	}
	if !result.OK {
		t.Fatalf("result = %+v", result)
	}
	if result.ContextUsage == nil || result.ContextUsage.Tokens == nil || *result.ContextUsage.Tokens != 8951 {
		t.Fatalf("unexpected context usage: %+v", result.ContextUsage)
	}
}

func TestRunControlRequiresTerminalResultFrame(t *testing.T) {
	outDir := t.TempDir()
	fakePi := writeFakePi(t, `
printf '%s\n' '{"type":"compaction_start","reason":"manual"}'
`)

	_, err := RunControl(context.Background(), ControlOptions{
		BinaryPath:  fakePi,
		CWD:         outDir,
		SessionFile: filepath.Join(outDir, "session.jsonl"),
		Command: ControlCommand{
			Type: ControlCommandCompact,
		},
	})
	if err == nil {
		t.Fatal("expected missing terminal frame error")
	}
	if !strings.Contains(err.Error(), "missing terminal result frame") {
		t.Fatalf("error = %v", err)
	}
}

func TestAutoThresholdPercentClampsSmallWindows(t *testing.T) {
	if got := AutoThresholdPercent(1000); got != 0 {
		t.Fatalf("threshold = %f", got)
	}
	if got := AutoThresholdPercent(1000000); got != 98.3616 {
		t.Fatalf("threshold = %f", got)
	}
}

func TestContextWindowForModel(t *testing.T) {
	tests := map[string]int64{
		"claude-opus-4-7":             longContextWindow,
		"claude-opus-4-6":             longContextWindow,
		"claude-opus-4-6[1m]":         longContextWindow,
		"anthropic/claude-sonnet-4-6": longContextWindow,
		"claude-sonnet-4-6:high":      longContextWindow,
		"claude-haiku-4-5-20251001":   defaultContextWindow,
		"anthropic/[custom-model]":    defaultContextWindow,
		"":                            defaultContextWindow,
	}

	for model, want := range tests {
		if got := ContextWindowForModel(model); got != want {
			t.Fatalf("ContextWindowForModel(%q) = %d, want %d", model, got, want)
		}
	}
}

func TestRunControlTimesOut(t *testing.T) {
	outDir := t.TempDir()
	fakePi := writeFakePi(t, `
sleep 2
`)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	_, err := RunControl(ctx, ControlOptions{
		BinaryPath:  fakePi,
		CWD:         outDir,
		SessionFile: filepath.Join(outDir, "session.jsonl"),
		Command: ControlCommand{
			Type: ControlCommandGetSessionStats,
		},
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
