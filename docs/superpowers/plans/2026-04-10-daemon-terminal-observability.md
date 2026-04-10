# Daemon Terminal Observability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `gsd-cloud start` show rich, color-coded terminal output of Claude's activity — tool calls, thinking, streamed text, task lifecycle, connection status, and idle heartbeat — with three verbosity levels.

**Architecture:** Port the display package from the spike (`gsd-cloud/apps/daemon/internal/display/`) into production (`gsd-build-daemon/internal/display/`) with design modifications (left-aligned, `→` prefix, dim tool calls, no task labels). Wire it into `actor.handleEvent` as the integration point. Verbosity flows from CLI flags → daemon → manager → actor.

**Tech Stack:** Go 1.25, standard library only (no external logging frameworks). ANSI escape codes for terminal formatting.

**Spec:** `docs/superpowers/specs/2026-04-10-daemon-terminal-observability-design.md`

**Spike source:** `/Users/lexchristopherson/Developer/gsd-aux/gsd-cloud/apps/daemon/internal/display/`

---

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `internal/display/display.go` | Create | `VerbosityLevel` enum, ANSI constants, HR/Truncate helpers, request banner, error banner |
| `internal/display/display_test.go` | Create | Tests for banners, truncation, HR |
| `internal/display/format.go` | Create | `FormatEvent(raw, level)`, `FormatEventSkipText(raw, level)`, tool display paths, result summary |
| `internal/display/format_test.go` | Create | Tests for all event type formatting at all verbosity levels |
| `internal/display/stream.go` | Create | `StreamHandler` for progressive text delta rendering |
| `internal/display/stream_test.go` | Create | Tests for stream event handling and skip/dedup logic |
| `internal/session/actor.go` | Modify | Add display fields, wire display into `handleEvent`, `SendTask`, `handleResult` |
| `internal/session/actor_test.go` | Modify | Existing tests still pass (display output is side-effect, does not affect relay behavior) |
| `internal/session/manager.go` | Modify | Accept and pass through `VerbosityLevel` |
| `internal/loop/daemon.go` | Modify | Accept `VerbosityLevel`, pass to manager, color-format connection messages, add idle heartbeat |
| `cmd/start.go` | Modify | Add `--quiet`/`--debug` flags, resolve to `VerbosityLevel`, pass to `loop.New()` |

---

### Task 1: Create `internal/display/display.go`

**Files:**
- Create: `internal/display/display.go`
- Test: `internal/display/display_test.go`

Port from spike `display.go` with modifications: remove all indentation from output, remove task label from banners, remove `TASK COMPLETE` banner (result summary block is sufficient).

- [ ] **Step 1: Write tests for display helpers**

```go
// internal/display/display_test.go
package display

import (
	"strings"
	"testing"
)

func TestTruncateShortString(t *testing.T) {
	got := Truncate("hello", 10)
	if got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
}

func TestTruncateLongString(t *testing.T) {
	got := Truncate("hello world this is long", 10)
	if len([]rune(got)) > 11 { // 10 + ellipsis
		t.Errorf("expected truncated string, got %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis suffix, got %q", got)
	}
}

func TestTruncateUnicode(t *testing.T) {
	got := Truncate("日本語テスト文字列データ", 5)
	runes := []rune(got)
	if len(runes) > 6 { // 5 + ellipsis
		t.Errorf("expected 6 runes max, got %d: %q", len(runes), got)
	}
}

func TestHR(t *testing.T) {
	got := HR("─", 5)
	if !strings.Contains(got, "─────") {
		t.Errorf("expected 5 dashes, got %q", got)
	}
	// Should contain ANSI dim codes
	if !strings.Contains(got, Dim) {
		t.Errorf("expected dim ANSI code")
	}
}

func TestFormatRequestBanner(t *testing.T) {
	got := FormatRequestBanner("Fix the login bug", "/home/user/project", "claude-sonnet-4-5-20250514")
	if !strings.Contains(got, "Fix the login bug") {
		t.Error("banner should contain prompt text")
	}
	if !strings.Contains(got, "cwd:") {
		t.Error("banner should contain cwd")
	}
	if !strings.Contains(got, "model:") {
		t.Error("banner should contain model")
	}
	// Should NOT contain "TASK" label
	if strings.Contains(got, "TASK") {
		t.Error("banner should not contain TASK label")
	}
}

func TestFormatRequestBannerTruncatesLongPrompt(t *testing.T) {
	long := strings.Repeat("a", 200)
	got := FormatRequestBanner(long, "", "")
	// Prompt should be truncated to 80 chars
	if strings.Contains(got, long) {
		t.Error("long prompt should be truncated")
	}
}

func TestFormatErrorBanner(t *testing.T) {
	got := FormatErrorBanner("claude exited with code 1")
	if !strings.Contains(got, "FAILED") {
		t.Error("error banner should contain FAILED")
	}
	if !strings.Contains(got, "claude exited with code 1") {
		t.Error("error banner should contain error message")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go test ./internal/display/ -v`
Expected: Compilation error — package does not exist yet.

- [ ] **Step 3: Implement display.go**

```go
// internal/display/display.go
package display

import "fmt"

// VerbosityLevel controls output density.
type VerbosityLevel int

const (
	Quiet   VerbosityLevel = iota // Connection status only
	Default                       // Compact tool calls, streamed text, one-line thinking
	Debug                         // Full thinking, tool results, input details
)

// ANSI escape codes
const (
	Reset   = "\033[0m"
	Bold    = "\033[1m"
	Dim     = "\033[2m"
	White   = "\033[37m"
	Cyan    = "\033[36m"
	Green   = "\033[32m"
	Yellow  = "\033[33m"
	Magenta = "\033[35m"
	Red     = "\033[31m"
	Gray    = "\033[90m"
	BgGreen = "\033[42m"
	BgRed   = "\033[41m"
	Black   = "\033[30m"
)

// HR returns a horizontal rule of the given character repeated n times.
func HR(char string, n int) string {
	s := ""
	for i := 0; i < n; i++ {
		s += char
	}
	return Dim + s + Reset
}

// Truncate shortens a string to max length, adding … if truncated.
func Truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

// FormatRequestBanner prints a request start banner. No "TASK" label —
// the prompt text is the label.
func FormatRequestBanner(prompt, cwd, model string) string {
	lines := []string{HR("─", 46)}
	lines = append(lines, Truncate(prompt, 80))
	if cwd != "" {
		lines = append(lines, fmt.Sprintf("%scwd: %s%s", Dim, cwd, Reset))
	}
	if model != "" {
		lines = append(lines, fmt.Sprintf("%smodel: %s%s", Dim, model, Reset))
	}
	lines = append(lines, HR("─", 46))
	result := ""
	for _, l := range lines {
		result += l + "\n"
	}
	return result
}

// FormatErrorBanner prints a task failure banner.
func FormatErrorBanner(errMsg string) string {
	return fmt.Sprintf("\n%s%s%s FAILED %s %s\n", BgRed, Black, Bold, Reset, errMsg)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go test ./internal/display/ -v`
Expected: All PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
git add internal/display/display.go internal/display/display_test.go
git commit -m "feat(display): add display package with verbosity levels, ANSI helpers, banners"
```

---

### Task 2: Create `internal/display/format.go`

**Files:**
- Create: `internal/display/format.go`
- Test: `internal/display/format_test.go`

Port from spike `format.go` with modifications: left-aligned (no 2-space indent on Claude text), replace `▶` with `→` for tool calls, replace `◆ thinking:` with plain dim text, remove `◀` result prefix in debug mode, no `TASK COMPLETE` banner in `formatResult`.

- [ ] **Step 1: Write tests for event formatting**

```go
// internal/display/format_test.go
package display

import (
	"strings"
	"testing"
)

func TestFormatEventQuietReturnsEmpty(t *testing.T) {
	raw := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`)
	got := FormatEvent(raw, Quiet)
	if got != "" {
		t.Errorf("quiet mode should return empty, got %q", got)
	}
}

func TestFormatEventAssistantText(t *testing.T) {
	raw := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"I found the bug."}]}}`)
	got := FormatEvent(raw, Default)
	if !strings.Contains(got, "I found the bug.") {
		t.Errorf("should contain text, got %q", got)
	}
	// Text should NOT be indented
	if strings.Contains(got, "  I found the bug.") {
		t.Error("text should not be indented")
	}
}

func TestFormatEventThinkingDefault(t *testing.T) {
	raw := []byte(`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"Let me analyze this deeply and carefully to find the root cause of the problem"}]}}`)
	got := FormatEvent(raw, Default)
	// Should be dim, truncated, one line
	if !strings.Contains(got, Dim) {
		t.Error("thinking should be dim in default mode")
	}
	if strings.Contains(got, "\n") {
		t.Error("thinking should be one line in default mode")
	}
}

func TestFormatEventThinkingDebug(t *testing.T) {
	raw := []byte(`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"Line one\nLine two\nLine three"}]}}`)
	got := FormatEvent(raw, Debug)
	// Should show full text, multi-line
	if !strings.Contains(got, "Line one") {
		t.Error("debug thinking should contain full text")
	}
	if !strings.Contains(got, "Line three") {
		t.Error("debug thinking should contain all lines")
	}
}

func TestFormatEventToolCall(t *testing.T) {
	raw := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"src/auth.ts"}}]}}`)
	got := FormatEvent(raw, Default)
	if !strings.Contains(got, "→") {
		t.Error("tool call should use → prefix")
	}
	if !strings.Contains(got, "Read") {
		t.Error("tool call should contain tool name")
	}
	if !strings.Contains(got, "src/auth.ts") {
		t.Error("tool call should contain file path")
	}
	if !strings.Contains(got, Dim) {
		t.Error("tool call should be dim")
	}
}

func TestFormatEventToolCallBash(t *testing.T) {
	raw := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"npm test"}}]}}`)
	got := FormatEvent(raw, Default)
	if !strings.Contains(got, "npm test") {
		t.Error("bash tool call should show command")
	}
}

func TestFormatEventToolCallDebugShowsInputs(t *testing.T) {
	raw := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"src/auth.ts","old_string":"< Date","new_string":"<= Date"}}]}}`)
	got := FormatEvent(raw, Debug)
	if !strings.Contains(got, "file_path") {
		t.Error("debug tool call should show input keys")
	}
	if !strings.Contains(got, "old_string") {
		t.Error("debug tool call should show all input keys")
	}
}

func TestFormatEventSystemInit(t *testing.T) {
	raw := []byte(`{"type":"system","subtype":"init","model":"claude-sonnet-4-5-20250514","tools":["Read","Write","Bash"]}`)
	// Hidden in default mode
	got := FormatEvent(raw, Default)
	if got != "" {
		t.Errorf("system init should be hidden in default mode, got %q", got)
	}
	// Shown in debug mode
	got = FormatEvent(raw, Debug)
	if !strings.Contains(got, "Session started") {
		t.Errorf("system init should show in debug mode, got %q", got)
	}
}

func TestFormatEventResult(t *testing.T) {
	raw := []byte(`{"type":"result","num_turns":5,"duration_ms":12300,"total_cost_usd":0.0312,"session_id":"sess_abc"}`)
	got := FormatEvent(raw, Default)
	if !strings.Contains(got, "Done") {
		t.Error("result should contain Done")
	}
	if !strings.Contains(got, "5 turns") {
		t.Error("result should contain turn count")
	}
	if !strings.Contains(got, "$0.0312") {
		t.Error("result should contain cost")
	}
	// Session ID should NOT show in default mode
	if strings.Contains(got, "sess_abc") {
		t.Error("session ID should not show in default mode")
	}
}

func TestFormatEventResultDebugShowsSession(t *testing.T) {
	raw := []byte(`{"type":"result","num_turns":5,"duration_ms":12300,"total_cost_usd":0.0312,"session_id":"sess_abc"}`)
	got := FormatEvent(raw, Debug)
	if !strings.Contains(got, "sess_abc") {
		t.Error("debug result should show session ID")
	}
}

func TestFormatEventUserToolResultDebug(t *testing.T) {
	raw := []byte(`{"type":"user","message":{"content":[{"type":"tool_result","content":"line1\nline2\nline3"}]}}`)
	// Hidden in default mode
	got := FormatEvent(raw, Default)
	if got != "" {
		t.Errorf("user tool result should be hidden in default, got %q", got)
	}
	// Shown in debug mode
	got = FormatEvent(raw, Debug)
	if !strings.Contains(got, "result") {
		t.Error("debug user tool result should show")
	}
}

func TestFormatEventSkipTextOmitsText(t *testing.T) {
	raw := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"},{"type":"tool_use","name":"Read","input":{"file_path":"a.ts"}}]}}`)
	got := FormatEventSkipText(raw, Default)
	if strings.Contains(got, "hello") {
		t.Error("skip text should omit text blocks")
	}
	if !strings.Contains(got, "Read") {
		t.Error("skip text should still show tool calls")
	}
}

func TestToolDisplayPath(t *testing.T) {
	cases := []struct {
		name     string
		input    map[string]interface{}
		expected string
	}{
		{"Read", map[string]interface{}{"file_path": "src/auth.ts"}, "src/auth.ts"},
		{"Bash", map[string]interface{}{"command": "npm test"}, "npm test"},
		{"Grep", map[string]interface{}{"pattern": "validateToken"}, `"validateToken"`},
		{"Unknown", map[string]interface{}{"query": "hello world"}, "hello world"},
	}
	for _, tc := range cases {
		got := toolDisplayPath(tc.name, tc.input)
		if got != tc.expected {
			t.Errorf("toolDisplayPath(%q, %v) = %q, want %q", tc.name, tc.input, got, tc.expected)
		}
	}
}

func TestAppendToolResult(t *testing.T) {
	cases := []struct {
		content  string
		expected string
	}{
		{"", "ok"},
		{"short", "short"},
		{"line1\nline2\nline3", "3 lines"},
	}
	for _, tc := range cases {
		got := AppendToolResult(tc.content)
		if !strings.Contains(got, tc.expected) {
			t.Errorf("AppendToolResult(%q) should contain %q, got %q", tc.content, tc.expected, got)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go test ./internal/display/ -v -run "Format|Tool|Append"`
Expected: Compilation error — functions not defined yet.

- [ ] **Step 3: Implement format.go**

```go
// internal/display/format.go
package display

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FormatEvent takes raw JSON bytes from Claude's stream-json output
// and returns a colored terminal string. Returns "" for events that
// should not be displayed at the given verbosity level.
func FormatEvent(raw []byte, level VerbosityLevel) string {
	if level == Quiet {
		return ""
	}

	var peek struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &peek) != nil {
		return ""
	}

	switch peek.Type {
	case "system":
		return formatSystem(raw, level)
	case "assistant":
		return formatAssistant(raw, level)
	case "user":
		return formatUser(raw, level)
	case "result":
		return formatResult(raw, level)
	default:
		return ""
	}
}

func formatSystem(raw []byte, level VerbosityLevel) string {
	if level != Debug {
		return ""
	}
	var ev struct {
		Subtype string   `json:"subtype"`
		Model   string   `json:"model"`
		Tools   []string `json:"tools"`
	}
	if json.Unmarshal(raw, &ev) != nil || ev.Subtype != "init" {
		return ""
	}
	return fmt.Sprintf("%sSession started | model: %s | %d tools%s",
		Dim, ev.Model, len(ev.Tools), Reset)
}

type contentBlock struct {
	Type     string                 `json:"type"`
	Text     string                 `json:"text"`
	Thinking string                 `json:"thinking"`
	Name     string                 `json:"name"`
	Input    map[string]interface{} `json:"input"`
	Content  json.RawMessage        `json:"content"`
}

func formatAssistant(raw []byte, level VerbosityLevel) string {
	var ev struct {
		Message struct {
			Content []contentBlock `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &ev) != nil {
		return ""
	}

	var parts []string
	for _, block := range ev.Message.Content {
		switch block.Type {
		case "thinking":
			text := block.Thinking
			if text == "" {
				text = block.Text
			}
			if text == "" {
				continue
			}
			if level == Debug {
				// Full thinking, multi-line, dim
				for _, line := range strings.Split(text, "\n") {
					parts = append(parts, fmt.Sprintf("%s%s%s", Dim, line, Reset))
				}
			} else {
				// One-line truncated, dim
				flat := strings.ReplaceAll(text, "\n", " ")
				parts = append(parts, fmt.Sprintf("%s%s%s", Dim, Truncate(flat, 80), Reset))
			}

		case "tool_use":
			path := toolDisplayPath(block.Name, block.Input)
			parts = append(parts, fmt.Sprintf("%s→ %s %s%s", Dim, block.Name, path, Reset))
			if level == Debug {
				for key, val := range block.Input {
					s := fmt.Sprintf("%v", val)
					if str, ok := val.(string); ok {
						s = str
					}
					parts = append(parts, fmt.Sprintf("%s  %s: %s%s",
						Dim, key, Truncate(s, 200), Reset))
				}
			}

		case "text":
			if strings.TrimSpace(block.Text) == "" {
				continue
			}
			// Full brightness, left-aligned
			for _, line := range strings.Split(block.Text, "\n") {
				parts = append(parts, line)
			}
		}
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n")
}

func formatUser(raw []byte, level VerbosityLevel) string {
	if level != Debug {
		return ""
	}

	var ev struct {
		Message struct {
			Content []contentBlock `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &ev) != nil {
		return ""
	}

	var parts []string
	for _, block := range ev.Message.Content {
		if block.Type != "tool_result" {
			continue
		}
		content := normalizeContent(block.Content)
		if strings.TrimSpace(content) == "" {
			continue
		}
		lines := strings.Split(content, "\n")
		const maxLines = 30
		parts = append(parts, fmt.Sprintf("%sresult (%d lines)%s", Dim, len(lines), Reset))
		display := lines
		if len(lines) > maxLines {
			display = lines[:maxLines]
			display = append(display, fmt.Sprintf("%s... (%d more lines)%s", Dim, len(lines)-maxLines, Reset))
		}
		for _, line := range display {
			parts = append(parts, fmt.Sprintf("%s%s%s", Dim, line, Reset))
		}
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n")
}

func normalizeContent(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var arr []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &arr) == nil {
		texts := make([]string, len(arr))
		for i, item := range arr {
			texts[i] = item.Text
		}
		return strings.Join(texts, "\n")
	}
	return string(raw)
}

func formatResult(raw []byte, level VerbosityLevel) string {
	var ev struct {
		NumTurns     int     `json:"num_turns"`
		DurationMs   float64 `json:"duration_ms"`
		TotalCostUsd float64 `json:"total_cost_usd"`
		SessionID    string  `json:"session_id"`
	}
	if json.Unmarshal(raw, &ev) != nil {
		return ""
	}

	cost := "?"
	if ev.TotalCostUsd > 0 {
		cost = fmt.Sprintf("$%.4f", ev.TotalCostUsd)
	}
	dur := "?"
	if ev.DurationMs > 0 {
		dur = fmt.Sprintf("%.1fs", ev.DurationMs/1000)
	}

	lines := []string{
		HR("═", 46),
		fmt.Sprintf("%sDone%s | %d turns | %s | %s", Bold, Reset, ev.NumTurns, dur, cost),
	}
	if level == Debug && ev.SessionID != "" {
		lines = append(lines, fmt.Sprintf("%ssession: %s%s", Dim, ev.SessionID, Reset))
	}
	lines = append(lines, HR("═", 46))
	return strings.Join(lines, "\n")
}

// toolDisplayPath extracts the most useful identifier from tool input.
func toolDisplayPath(name string, input map[string]interface{}) string {
	switch name {
	case "Read", "Write", "Edit", "Glob":
		if fp, ok := input["file_path"].(string); ok {
			return fp
		}
	case "Bash":
		if cmd, ok := input["command"].(string); ok {
			return Truncate(cmd, 60)
		}
	case "Grep":
		if pat, ok := input["pattern"].(string); ok {
			return fmt.Sprintf("%q", pat)
		}
	}
	for _, v := range input {
		if s, ok := v.(string); ok {
			return Truncate(s, 60)
		}
	}
	return ""
}

// toolResultSummary produces a compact summary of a tool result.
func toolResultSummary(content string) string {
	if content == "" {
		return "ok"
	}
	lines := strings.Split(content, "\n")
	if len(lines) == 1 {
		if len(content) < 60 {
			return content
		}
		return Truncate(content, 60)
	}
	return fmt.Sprintf("%d lines", len(lines))
}

// AppendToolResult returns a formatted " → <summary>" suffix.
func AppendToolResult(content string) string {
	summary := toolResultSummary(content)
	return fmt.Sprintf("  %s%s%s", Green, summary, Reset)
}

// FormatEventSkipText formats an assistant event but omits text blocks.
// Used when text was already rendered via streaming deltas.
func FormatEventSkipText(raw []byte, level VerbosityLevel) string {
	if level == Quiet {
		return ""
	}

	var ev struct {
		Type    string `json:"type"`
		Message struct {
			Content []contentBlock `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &ev) != nil || ev.Type != "assistant" {
		return ""
	}

	var parts []string
	for _, block := range ev.Message.Content {
		switch block.Type {
		case "thinking":
			text := block.Thinking
			if text == "" {
				text = block.Text
			}
			if text == "" {
				continue
			}
			if level == Debug {
				for _, line := range strings.Split(text, "\n") {
					parts = append(parts, fmt.Sprintf("%s%s%s", Dim, line, Reset))
				}
			} else {
				flat := strings.ReplaceAll(text, "\n", " ")
				parts = append(parts, fmt.Sprintf("%s%s%s", Dim, Truncate(flat, 80), Reset))
			}
		case "tool_use":
			path := toolDisplayPath(block.Name, block.Input)
			parts = append(parts, fmt.Sprintf("%s→ %s %s%s", Dim, block.Name, path, Reset))
		}
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go test ./internal/display/ -v`
Expected: All PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
git add internal/display/format.go internal/display/format_test.go
git commit -m "feat(display): add event formatter with tool paths, result summaries, verbosity levels"
```

---

### Task 3: Create `internal/display/stream.go`

**Files:**
- Create: `internal/display/stream.go`
- Test: `internal/display/stream_test.go`

Port from spike `stream.go` with modification: no indentation prefix on `content_block_start` (print `\n` not `\n  `).

- [ ] **Step 1: Write tests for stream handler**

```go
// internal/display/stream_test.go
package display

import (
	"bytes"
	"testing"
)

func TestIsStreamEvent(t *testing.T) {
	if !IsStreamEvent([]byte(`{"type":"stream_event","event":{}}"`)) {
		t.Error("should detect stream_event")
	}
	if IsStreamEvent([]byte(`{"type":"assistant"}`)) {
		t.Error("should not detect non-stream_event")
	}
}

func TestStreamHandlerTextDeltas(t *testing.T) {
	var buf bytes.Buffer
	sh := NewStreamHandler(&buf, Default)

	// content_block_start for text
	sh.Handle([]byte(`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"text"}}}`))

	// text deltas
	sh.Handle([]byte(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello "}}}`))
	sh.Handle([]byte(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"world"}}}`))

	// content_block_stop
	sh.Handle([]byte(`{"type":"stream_event","event":{"type":"content_block_stop"}}`))

	got := buf.String()
	if got != "\nHello world\n" {
		t.Errorf("expected '\\nHello world\\n', got %q", got)
	}
}

func TestStreamHandlerQuietSuppresses(t *testing.T) {
	var buf bytes.Buffer
	sh := NewStreamHandler(&buf, Quiet)

	sh.Handle([]byte(`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"text"}}}`))
	sh.Handle([]byte(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}}`))
	sh.Handle([]byte(`{"type":"stream_event","event":{"type":"content_block_stop"}}`))

	if buf.Len() != 0 {
		t.Errorf("quiet mode should suppress all output, got %q", buf.String())
	}
}

func TestStreamHandlerShouldSkipText(t *testing.T) {
	var buf bytes.Buffer
	sh := NewStreamHandler(&buf, Default)

	if sh.ShouldSkipText() {
		t.Error("should not skip before any streaming")
	}

	sh.Handle([]byte(`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"text"}}}`))
	if !sh.ShouldSkipText() {
		t.Error("should skip during active streaming")
	}

	sh.Handle([]byte(`{"type":"stream_event","event":{"type":"content_block_stop"}}`))
	if !sh.ShouldSkipText() {
		t.Error("should skip after streaming (before consume)")
	}

	sh.ConsumeSkip()
	if sh.ShouldSkipText() {
		t.Error("should not skip after consume")
	}
}

func TestStreamHandlerIgnoresNonTextBlocks(t *testing.T) {
	var buf bytes.Buffer
	sh := NewStreamHandler(&buf, Default)

	// A thinking block start should not trigger streaming
	sh.Handle([]byte(`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"thinking"}}}`))
	if sh.ShouldSkipText() {
		t.Error("thinking blocks should not activate streaming")
	}
	if buf.Len() != 0 {
		t.Errorf("thinking block should not produce output, got %q", buf.String())
	}
}

func TestStreamHandlerReturnsFalseForNonStreamEvents(t *testing.T) {
	var buf bytes.Buffer
	sh := NewStreamHandler(&buf, Default)

	handled := sh.Handle([]byte(`{"type":"assistant","message":{}}`))
	if handled {
		t.Error("should return false for non-stream events")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go test ./internal/display/ -v -run "Stream|IsStream"`
Expected: Compilation error — StreamHandler not defined.

- [ ] **Step 3: Implement stream.go**

```go
// internal/display/stream.go
package display

import (
	"encoding/json"
	"fmt"
	"io"
)

// StreamHandler processes stream_event JSON for progressive text rendering.
type StreamHandler struct {
	w        io.Writer
	level    VerbosityLevel
	active   bool
	skipNext bool
}

// NewStreamHandler creates a handler that writes streaming text to w.
func NewStreamHandler(w io.Writer, level VerbosityLevel) *StreamHandler {
	return &StreamHandler{w: w, level: level}
}

// IsStreamEvent returns true if the raw JSON is a stream_event.
func IsStreamEvent(raw []byte) bool {
	var peek struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &peek) != nil {
		return false
	}
	return peek.Type == "stream_event"
}

// Handle processes a stream_event JSON line. Returns true if it was handled.
func (sh *StreamHandler) Handle(raw []byte) bool {
	if !IsStreamEvent(raw) {
		return false
	}

	var ev struct {
		Event struct {
			Type         string `json:"type"`
			ContentBlock struct {
				Type string `json:"type"`
			} `json:"content_block"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		} `json:"event"`
	}
	if json.Unmarshal(raw, &ev) != nil {
		return true
	}

	switch ev.Event.Type {
	case "content_block_start":
		if ev.Event.ContentBlock.Type == "text" {
			sh.active = true
			sh.skipNext = false
			if sh.level != Quiet {
				fmt.Fprint(sh.w, "\n")
			}
		}

	case "content_block_delta":
		if sh.active && ev.Event.Delta.Type == "text_delta" && sh.level != Quiet {
			fmt.Fprint(sh.w, ev.Event.Delta.Text)
		}

	case "content_block_stop":
		if sh.active {
			if sh.level != Quiet {
				fmt.Fprint(sh.w, "\n")
			}
			sh.active = false
			sh.skipNext = true
		}
	}

	return true
}

// ShouldSkipText returns true if assistant text blocks should be skipped
// because they were already rendered via streaming deltas.
func (sh *StreamHandler) ShouldSkipText() bool {
	return sh.skipNext || sh.active
}

// ConsumeSkip resets the skip flag after it has been checked.
func (sh *StreamHandler) ConsumeSkip() {
	sh.skipNext = false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go test ./internal/display/ -v`
Expected: All PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
git add internal/display/stream.go internal/display/stream_test.go
git commit -m "feat(display): add stream handler for progressive text delta rendering"
```

---

### Task 4: Wire display into `session/actor.go`

**Files:**
- Modify: `internal/session/actor.go`
- Verify: `internal/session/actor_test.go` (existing tests still pass)

Add display fields to Actor. In `handleEvent`: route stream_events to StreamHandler, format and print other events. In `SendTask`: print request banner. In `handleResult`: print result summary or error banner.

- [ ] **Step 1: Run existing actor tests to establish baseline**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go test ./internal/session/ -v -count=1`
Expected: All PASS.

- [ ] **Step 2: Add display fields to Options and Actor**

In `internal/session/actor.go`, add the import and fields:

Add to imports:
```go
"os"

"github.com/gsd-build/daemon/internal/display"
```

Add field to `Options` struct (after `StartSeq int64`):
```go
Verbosity display.VerbosityLevel
```

Add fields to `Actor` struct (after `stopCh chan struct{}`):
```go
verbosity display.VerbosityLevel
stream    *display.StreamHandler
```

In `NewActor`, after `a.seq.Store(opts.StartSeq)`, add:
```go
a.verbosity = opts.Verbosity
a.stream = display.NewStreamHandler(os.Stdout, opts.Verbosity)
```

- [ ] **Step 3: Wire display into handleEvent**

Replace the `handleEvent` method body with:

```go
func (a *Actor) handleEvent(e claude.Event) error {
	next := a.seq.Add(1)

	// Display to terminal before WAL/relay so user sees events ASAP.
	// Display errors are swallowed — they must never interrupt the relay pipeline.
	if a.stream.Handle(e.Raw) {
		// stream_event was handled (progressive text delta)
	} else if a.stream.ShouldSkipText() {
		// Text was already streamed; show non-text parts only
		if s := display.FormatEventSkipText(e.Raw, a.verbosity); s != "" {
			fmt.Println(s)
		}
		if e.Type != "assistant" {
			a.stream.ConsumeSkip()
		}
	} else {
		if s := display.FormatEvent(e.Raw, a.verbosity); s != "" {
			fmt.Println(s)
		}
	}

	// Every event becomes a stream frame
	frame := &protocol.Stream{
		Type:           protocol.MsgTypeStream,
		SessionID:      a.opts.SessionID,
		ChannelID:      a.opts.ChannelID,
		SequenceNumber: next,
		Event:          e.Raw,
	}

	frameJSON, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("marshal frame: %w", err)
	}
	if err := a.log.Append(next, frameJSON); err != nil {
		return fmt.Errorf("wal append: %w", err)
	}
	if err := a.opts.Relay.Send(frame); err != nil {
		// Best-effort — WAL has the entry, relay reconnect will replay it
		return nil
	}

	// On result events, also emit taskComplete
	if e.Type == "result" {
		return a.handleResult(e.Raw)
	}
	return nil
}
```

- [ ] **Step 4: Wire display into SendTask**

In `SendTask`, after `a.taskInFlight.Store(tc)` and before the relay send, add:

```go
if a.verbosity != display.Quiet {
	fmt.Print(display.FormatRequestBanner(task.Prompt, a.opts.CWD, a.opts.Model))
}
```

- [ ] **Step 5: Wire display into handleResult for permission/question display**

In `handleResult`, the result summary is already handled by `handleEvent` calling `FormatEvent` on the result event — no additional display call needed for normal completion.

For permission denials: in the `if len(payload.PermissionDenials) > 0` block, after each `a.opts.Relay.Send(&protocol.PermissionRequest{...})` call, add terminal display:

```go
if a.verbosity != display.Quiet {
	fmt.Printf("%s⚠ PERMISSION REQUEST: %s%s\n", display.Yellow, denial.ToolName, display.Reset)
	if cmd, ok := toolInputSummary(denial.ToolInput); ok {
		fmt.Printf("%s%s%s\n", display.Dim, cmd, display.Reset)
	}
	fmt.Printf("%swaiting for approval...%s\n", display.Dim, display.Reset)
}
```

After each `a.opts.Relay.Send(&protocol.Question{...})` call:

```go
if a.verbosity != display.Quiet {
	fmt.Printf("%s? %s%s\n", display.Cyan, questionText, display.Reset)
	for i, opt := range optionLabels {
		fmt.Printf("%s  %d) %s%s\n", display.Dim, i+1, opt, display.Reset)
	}
	fmt.Printf("%swaiting for answer...%s\n", display.Dim, display.Reset)
}
```

Add a helper function to `actor.go`:

```go
func toolInputSummary(raw json.RawMessage) (string, bool) {
	var input map[string]interface{}
	if json.Unmarshal(raw, &input) != nil {
		return "", false
	}
	if cmd, ok := input["command"].(string); ok {
		return "command: " + cmd, true
	}
	if fp, ok := input["file_path"].(string); ok {
		return "file_path: " + fp, true
	}
	return "", false
}
```

Also in `HandlePermissionResponse`, after `Approved — resuming`:

```go
if a.verbosity != display.Quiet {
	if resp.Approved {
		fmt.Printf("\n%sApproved — resuming.%s\n\n", display.Green, display.Reset)
	}
}
```

And in `HandleQuestionResponse`, after receiving answer:

```go
if a.verbosity != display.Quiet {
	fmt.Printf("\n%sAnswer: %s%s\n\n", display.Green, resp.Answer, display.Reset)
}
```

For the error case: in `manager.go`'s Run goroutine (the one that logs `[session] actor.Run exited with error`), add error banner display. This is covered in Task 6.

- [ ] **Step 6: Run existing actor tests to verify no regressions**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go test ./internal/session/ -v -count=1`
Expected: All PASS. The display output goes to stdout as a side-effect but does not affect relay frame assertions.

- [ ] **Step 7: Commit**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
git add internal/session/actor.go
git commit -m "feat(display): wire terminal display into actor event pipeline"
```

---

### Task 5: Pass verbosity through `session/manager.go`

**Files:**
- Modify: `internal/session/manager.go`

Manager needs to store the verbosity level and set it on every actor's Options.

- [ ] **Step 1: Add verbosity to Manager**

Add import:
```go
"github.com/gsd-build/daemon/internal/display"
```

Add field to `Manager` struct (after `binaryPath string`):
```go
verbosity display.VerbosityLevel
```

Update `NewManager` signature and body:
```go
func NewManager(baseWALDir, binaryPath string, relay RelaySender, verbosity display.VerbosityLevel) *Manager {
	return &Manager{
		actors:     make(map[string]*Actor),
		baseWALDir: baseWALDir,
		relay:      relay,
		binaryPath: binaryPath,
		verbosity:  verbosity,
	}
}
```

In `Spawn`, after `opts.BinaryPath = m.binaryPath` (inside the defaults block), add:
```go
opts.Verbosity = m.verbosity
```

In the `Spawn` goroutine that logs `[session] actor.Run exited with error`, add an error banner display after the existing `log.Printf`:
```go
if m.verbosity != display.Quiet {
	fmt.Print(display.FormatErrorBanner(err.Error()))
}
```

Add `"fmt"` to imports if not already present.

- [ ] **Step 2: Fix compilation in manager_test.go**

The existing `manager_test.go` likely calls `NewManager` with the old 3-arg signature. Read and check. If needed, add the `display.Quiet` argument (tests don't need terminal output). Check if there's a direct `NewManager` call in the test file.

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && grep -n "NewManager" internal/session/manager_test.go`

If found, update each call to include `display.Default` (or `display.Quiet`) as the 4th argument.

- [ ] **Step 3: Fix compilation in daemon.go**

In `internal/loop/daemon.go`, the `NewWithBinaryPath` function calls `session.NewManager(walDir, binaryPath, client)`. Update to pass `display.Default` temporarily (will be replaced with actual verbosity in Task 6):

```go
manager := session.NewManager(walDir, binaryPath, client, display.Default)
```

Add import:
```go
"github.com/gsd-build/daemon/internal/display"
```

- [ ] **Step 4: Run all tests to verify no regressions**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go test ./... -count=1`
Expected: All PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
git add internal/session/manager.go internal/loop/daemon.go
git commit -m "feat(display): pass verbosity level through session manager to actors"
```

Note: also `git add` any test file that needed the signature fix.

---

### Task 6: Wire verbosity and heartbeat into `loop/daemon.go`

**Files:**
- Modify: `internal/loop/daemon.go`
- Modify: `cmd/start.go`

Accept verbosity in daemon constructor, pass to manager, color-format connection lifecycle messages, add idle heartbeat.

- [ ] **Step 1: Accept verbosity in daemon constructor**

Update `Daemon` struct — add field (after `walDir string`):
```go
verbosity display.VerbosityLevel
```

Update `New` signature:
```go
func New(cfg *config.Config, version string, verbosity display.VerbosityLevel) (*Daemon, error) {
	return NewWithBinaryPath(cfg, version, "claude", verbosity)
}
```

Update `NewWithBinaryPath` signature and body:
```go
func NewWithBinaryPath(cfg *config.Config, version, binaryPath string, verbosity display.VerbosityLevel) (*Daemon, error) {
```

Update the `session.NewManager` call inside `NewWithBinaryPath`:
```go
manager := session.NewManager(walDir, binaryPath, client, verbosity)
```

Set the field on the Daemon:
```go
return &Daemon{
	cfg:       cfg,
	version:   version,
	manager:   manager,
	client:    client,
	walDir:    walDir,
	verbosity: verbosity,
}, nil
```

- [ ] **Step 2: Color-format connection lifecycle messages in Run**

In the `Run` method, replace the plain `fmt.Printf` connection messages:

Replace:
```go
fmt.Printf("relay connection lost: %v\n", err)
fmt.Printf("reconnecting in %s...\n", backoff)
```

With:
```go
fmt.Printf("%srelay disconnected (%s) — %v%s\n", display.Dim, time.Since(connStart).Truncate(time.Second), err, display.Reset)
fmt.Printf("%sreconnecting in %s...%s\n", display.Dim, backoff, display.Reset)
```

- [ ] **Step 3: Add relay connected message in runOnce**

In `runOnce`, after the successful `d.client.Connect` call and before the heartbeat/token goroutines, add:
```go
fmt.Printf("%srelay connected%s\n", display.Dim, display.Reset)
```

- [ ] **Step 4: Add idle heartbeat goroutine**

Add a new method to Daemon:

```go
func (d *Daemon) runIdleHeartbeat(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now().Format("15:04")
			fmt.Printf("%s%s ♥ connected · idle%s\n", display.Dim, now, display.Reset)
		}
	}
}
```

In `runOnce`, add the idle heartbeat goroutine alongside the existing heartbeat and token refresh goroutines:
```go
go d.runIdleHeartbeat(connCtx)
```

- [ ] **Step 5: Update cmd/start.go — add flags and pass verbosity**

Replace `cmd/start.go` with:

```go
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/gsd-build/daemon/internal/config"
	"github.com/gsd-build/daemon/internal/display"
	"github.com/gsd-build/daemon/internal/loop"
	"github.com/spf13/cobra"
)

var (
	flagQuiet bool
	flagDebug bool
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the daemon and connect to GSD Cloud",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("not paired — run `gsd-cloud login` first: %w", err)
		}

		verbosity := display.Default
		if flagDebug {
			verbosity = display.Debug
		} else if flagQuiet {
			verbosity = display.Quiet
		}

		d, err := loop.New(cfg, Version, verbosity)
		if err != nil {
			return fmt.Errorf("init daemon: %w", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sigCh
			fmt.Println("\nShutting down...")
			cancel()
		}()

		fmt.Printf("Connecting to %s as %s...\n", cfg.RelayURL, cfg.MachineID)
		if err := d.Run(ctx); err != nil && err != context.Canceled {
			return err
		}
		return nil
	},
}

func init() {
	startCmd.Flags().BoolVar(&flagQuiet, "quiet", false, "Connection status and heartbeat only")
	startCmd.Flags().BoolVar(&flagDebug, "debug", false, "Full event details (thinking, tool results, inputs)")
	rootCmd.AddCommand(startCmd)
}
```

- [ ] **Step 6: Fix compilation in daemon_test.go**

The `daemon_test.go` does not directly call `New` or `NewWithBinaryPath` (it only calls `buildRelayURL`), so it should compile without changes. Verify by checking:

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && grep -n "NewWithBinaryPath\|loop.New" internal/loop/daemon_test.go tests/e2e/daemon_e2e_test.go`

If any test file calls the old signatures, add `display.Default` as the verbosity argument.

- [ ] **Step 7: Run all tests**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go test ./... -count=1`
Expected: All PASS.

- [ ] **Step 8: Commit**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
git add cmd/start.go internal/loop/daemon.go
git commit -m "feat(display): add --quiet/--debug flags, connection lifecycle formatting, idle heartbeat"
```

Note: also `git add` any test files that needed signature fixes.

---

### Task 7: Verify end-to-end and fix compilation across the full codebase

**Files:**
- Verify: all files in the repo compile and tests pass

- [ ] **Step 1: Run full build**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go build ./...`
Expected: Clean build, no errors.

- [ ] **Step 2: Run full test suite**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go test ./... -count=1`
Expected: All PASS.

- [ ] **Step 3: Run vet**

Run: `cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon && go vet ./...`
Expected: No issues.

- [ ] **Step 4: Fix any issues found**

If any compilation errors, test failures, or vet warnings are found, fix them. Common issues:
- E2E test at `tests/e2e/daemon_e2e_test.go` may call `loop.NewWithBinaryPath` with old signature
- Any file importing `session` package that constructs `Options` directly

- [ ] **Step 5: Commit any fixes**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
git add -A
git commit -m "fix: resolve compilation issues from display integration"
```

Only run this step if Step 4 required changes.
