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

// AppendToolResult returns a formatted "  <summary>" suffix.
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
