// Integration test for pi.Executor with ask_human and extension_ui_request.
//
// Validates that:
//   1. The agent calls ask_human, which fires extension_ui_request[method=input]
//      from pi.
//   2. The executor intercepts the request and routes it through onUIRequest.
//   3. The handler's returned string is written back to pi as
//      extension_ui_response, the tool unblocks, and the agent finishes
//      with the answer integrated into its final response.
//   4. The full round-trip happens within one Run() call, with a single
//      pi process and observable cost rollup at the end.
//
// Run with:
//   go test -tags=integration ./internal/pi -v -run TestPiExecutor_AskHuman -timeout 90s

//go:build integration

package pi

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gsd-build/daemon/internal/claude"
)

func TestPiExecutor_AskHuman_RoundTrip(t *testing.T) {
	if _, err := exec.LookPath("pi"); err != nil {
		t.Skip("pi binary not on PATH")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude binary not on PATH")
	}

	_, thisFile, _, _ := runtime.Caller(0)
	pkgDir := filepath.Dir(thisFile)
	extPath := filepath.Join(pkgDir, "extension", "index.ts")

	const answerText = "Use approach B (the Anthropic-compat HTTP path). Simpler to ship and the cost difference is negligible."

	piExec := NewExecutor(Options{
		CWD:           t.TempDir(),
		Model:         "claude-sonnet-4-6",
		Provider:      "claude-cli",
		ExtensionPath: extPath,
		Prompt: strings.Join([]string{
			"I'm building a coding-agent harness on top of pi. Two approaches for multi-provider:",
			"  A) Spawn each provider's CLI binary as a subprocess and translate stdio.",
			"  B) Use the Anthropic-compat HTTP endpoint each provider offers.",
			"",
			"Use the ask_human tool to ask me which approach to take.",
			"Then in ONE short sentence (no preamble) state my choice back followed by the literal phrase 'DECISION CONFIRMED'.",
		}, "\n"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var (
		mu          sync.Mutex
		events      []claude.Event
		uiQueries   []UIRequest
		askedAt     time.Time
		answeredAt  time.Time
		resultEvent *claude.Event
	)

	onEvent := func(e claude.Event) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
		if e.Type == "result" {
			cp := e
			resultEvent = &cp
		}
		return nil
	}

	onUI := func(req UIRequest) (string, error) {
		mu.Lock()
		uiQueries = append(uiQueries, req)
		askedAt = time.Now()
		mu.Unlock()
		// Simulate phone-typing delay so we know the executor genuinely
		// waited for the answer rather than racing past it.
		time.Sleep(2 * time.Second)
		mu.Lock()
		answeredAt = time.Now()
		mu.Unlock()
		return answerText, nil
	}

	if err := piExec.Run(ctx, onEvent, onUI); err != nil {
		t.Fatalf("pi run failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(uiQueries) != 1 {
		t.Fatalf("expected exactly 1 UI request, got %d", len(uiQueries))
	}
	q := uiQueries[0]
	t.Logf("ui request: id=%s method=%s title=%q", q.ID, q.Method, truncateForLog(q.Title, 120))
	if q.Method != "input" {
		t.Errorf("ui request method=%q want input", q.Method)
	}
	if q.Title == "" {
		t.Error("ui request title is empty")
	}

	if resultEvent == nil {
		t.Fatal("missing synthesized result event")
	}

	delay := answeredAt.Sub(askedAt)
	if delay < 1500*time.Millisecond {
		t.Errorf("handler delay too short, got %v (want >= 1.5s)", delay)
	}

	var r struct {
		Type         string  `json:"type"`
		TotalCostUSD float64 `json:"total_cost_usd"`
		Usage        struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(resultEvent.Raw, &r); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	t.Logf("synthesized result: cost=$%.6f input=%d output=%d", r.TotalCostUSD, r.Usage.InputTokens, r.Usage.OutputTokens)
	if r.Usage.OutputTokens == 0 {
		t.Error("expected non-zero output tokens")
	}

	var finalText strings.Builder
	for _, e := range events {
		if e.Type != "stream_event" {
			continue
		}
		var top struct {
			Event struct {
				Type  string `json:"type"`
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			} `json:"event"`
		}
		_ = json.Unmarshal(e.Raw, &top)
		if top.Event.Type == "content_block_delta" && top.Event.Delta.Type == "text_delta" {
			finalText.WriteString(top.Event.Delta.Text)
		}
	}
	answer := finalText.String()
	t.Logf("agent final text (%d chars): %q", len(answer), truncateForLog(answer, 200))

	if !strings.Contains(answer, "DECISION CONFIRMED") {
		t.Errorf("agent final text missing 'DECISION CONFIRMED'; got: %q", answer)
	}
	if !strings.Contains(answer, "B") && !strings.Contains(answer, "Anthropic-compat") && !strings.Contains(answer, "HTTP") {
		t.Errorf("agent final text doesn't reference our answer (B/Anthropic-compat/HTTP); got: %q", answer)
	}

	byType := map[string]int{}
	for _, e := range events {
		byType[e.Type]++
	}
	for ty, n := range byType {
		t.Logf("  %-15s %d", ty, n)
	}
}

func truncateForLog(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
