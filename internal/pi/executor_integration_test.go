// Integration test for the pi executor.
//
// Validates that the pi executor:
//   1) spawns pi successfully against the vendored claude-sdk extension
//   2) streams pi NDJSON events through onEvent
//   3) emits a synthesized stream-json `result` event with non-zero usage/cost
//      pulled from pi's agent_end
//
// Skipped automatically if `pi` or `claude` binaries are not on PATH so the
// test suite stays portable. Run manually:
//   go test ./internal/pi -tags=integration -v -run TestPiExecutor
//
// Requires:
//   - pi >= 0.57 (https://github.com/mariozechner/pi-coding-agent)
//   - claude binary >= 2.1.119 (Claude Code CLI)
//   - npm install ran in internal/pi/extension/

//go:build integration

package pi

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/gsd-build/daemon/internal/claude"
)

func TestPiExecutor_RealPi_FactorialPrompt(t *testing.T) {
	if _, err := exec.LookPath("pi"); err != nil {
		t.Skip("pi binary not on PATH; skipping integration test")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude binary not on PATH; skipping integration test")
	}

	_, thisFile, _, _ := runtime.Caller(0)
	pkgDir := filepath.Dir(thisFile)
	extPath := filepath.Join(pkgDir, "extension", "index.ts")

	exec := NewExecutor(Options{
		CWD:           t.TempDir(),
		Model:         "claude-sonnet-4-6",
		Provider:      "claude-cli",
		ExtensionPath: extPath,
		Prompt:        "Reply with one short sentence: what is 2 + 2?",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var (
		mu           sync.Mutex
		events       []claude.Event
		seenAgentEnd bool
		resultEvent  *claude.Event
	)

	err := exec.Run(ctx, func(e claude.Event) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
		if e.Type == "agent_end" {
			seenAgentEnd = true
		}
		if e.Type == "result" {
			cp := e
			resultEvent = &cp
		}
		return nil
	}, nil) // no UI handler; this test doesn't fire ask_human
	if err != nil {
		t.Fatalf("pi run failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(events) == 0 {
		t.Fatal("no events received from pi")
	}
	t.Logf("received %d total events", len(events))

	// Tally event types.
	byType := map[string]int{}
	for _, e := range events {
		byType[e.Type]++
	}
	for ty, n := range byType {
		t.Logf("  %-25s %d", ty, n)
	}

	// The executor emits Claude-shaped stream_event, system, user, and result
	// events. agent_end is consumed internally to fire the synthesized result.
	if resultEvent == nil {
		t.Fatal("missing synthesized result event")
	}
	if byType["system"] < 1 {
		t.Errorf("expected at least 1 system event (translated from pi session), got %d", byType["system"])
	}
	if byType["stream_event"] < 1 {
		t.Errorf("expected at least 1 stream_event (translated from pi message_update / tool_execution), got %d", byType["stream_event"])
	}
	_ = seenAgentEnd

	// Decode the synthesized result and check the rolled-up usage/cost.
	var r struct {
		Type         string  `json:"type"`
		TotalCostUSD float64 `json:"total_cost_usd"`
		Usage        struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			CacheReadInput     int `json:"cache_read_input_tokens"`
			CacheCreationInput int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
		PermissionDenials any `json:"permission_denials"`
	}
	if err := json.Unmarshal(resultEvent.Raw, &r); err != nil {
		t.Fatalf("decode synthesized result: %v", err)
	}
	if r.Type != "result" {
		t.Errorf("synthesized result has wrong type: %q", r.Type)
	}
	if r.Usage.OutputTokens == 0 {
		t.Errorf("synthesized result has zero output tokens; expected non-zero")
	}
	// Cost may be 0 if the model returns instantly with cached tokens only.
	// Just log it; don't gate the test on it.
	t.Logf("synthesized result: cost=$%.6f input=%d output=%d cacheRead=%d cacheWrite=%d",
		r.TotalCostUSD,
		r.Usage.InputTokens,
		r.Usage.OutputTokens,
		r.Usage.CacheReadInput,
		r.Usage.CacheCreationInput,
	)
	if r.PermissionDenials != nil {
		t.Errorf("permission_denials should be omitted/null for pi; got %+v", r.PermissionDenials)
	}
}
