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
	if strings.Contains(got, "  I found the bug.") {
		t.Error("text should not be indented")
	}
}

func TestFormatEventThinkingDefault(t *testing.T) {
	raw := []byte(`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"Let me analyze this deeply and carefully to find the root cause of the problem"}]}}`)
	got := FormatEvent(raw, Default)
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
}

func TestFormatEventSystemInit(t *testing.T) {
	raw := []byte(`{"type":"system","subtype":"init","model":"claude-sonnet-4-5-20250514","tools":["Read","Write","Bash"]}`)
	got := FormatEvent(raw, Default)
	if got != "" {
		t.Errorf("system init should be hidden in default mode, got %q", got)
	}
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
	got := FormatEvent(raw, Default)
	if got != "" {
		t.Errorf("user tool result should be hidden in default, got %q", got)
	}
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
