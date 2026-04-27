package pi

import (
	"context"
	"encoding/json"
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
printf '%s\n' '{"type":"control_result","ok":true,"contextUsage":{"tokens":270000,"contextWindow":1000000,"percent":27}}'
`)

	result, err := RunControl(context.Background(), ControlOptions{
		BinaryPath:  fakePi,
		CWD:         outDir,
		SessionFile: filepath.Join(outDir, "session.jsonl"),
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
	if result.ContextUsage == nil || result.ContextUsage.ContextWindow != 1000000 {
		encoded, _ := json.Marshal(result)
		t.Fatalf("unexpected result: %s", encoded)
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
