// fake-claude is a test helper that mimics `claude -p --output-format stream-json`.
// It reads the prompt from the last positional CLI argument, emits scripted
// stream-json events to stdout, and exits.
//
// Env vars:
//
//	FAKE_CLAUDE_DENY_TOOL=<name>  — emit permission_denials in the result
//	                                (unless the tool is in --allowedTools)
//	FAKE_CLAUDE_SESSION_ID=<id>   — override the synthetic session id
//	FAKE_CLAUDE_ARGS_FILE=<path>  — write os.Args[1:] as JSON to this file
//	FAKE_CLAUDE_STDERR=<text>     — write this string to stderr at startup
//	FAKE_CLAUDE_EXIT_CODE=<n>     — exit with this code immediately after stderr
//
// Usage: invoked by the daemon as `claude -p ... "prompt"` during tests.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func main() {
	// Write argv to file if requested
	if argsFile := os.Getenv("FAKE_CLAUDE_ARGS_FILE"); argsFile != "" {
		data, _ := json.Marshal(os.Args[1:])
		_ = os.WriteFile(argsFile, data, 0o600)
	}

	// Stderr and exit code hooks
	if msg := os.Getenv("FAKE_CLAUDE_STDERR"); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}
	if codeStr := os.Getenv("FAKE_CLAUDE_EXIT_CODE"); codeStr != "" {
		if code, err := strconv.Atoi(codeStr); err == nil {
			os.Exit(code)
		}
	}

	denyTool := os.Getenv("FAKE_CLAUDE_DENY_TOOL")
	sessionID := os.Getenv("FAKE_CLAUDE_SESSION_ID")
	if sessionID == "" {
		sessionID = "fake-session-123"
	}

	// Detect --allowedTools
	allowedSet := make(map[string]bool)
	for i, arg := range os.Args {
		if arg == "--allowedTools" && i+1 < len(os.Args) {
			for _, t := range strings.Split(os.Args[i+1], ",") {
				allowedSet[strings.TrimSpace(t)] = true
			}
		}
	}

	// Emit events to stdout

	// 1. stream_event partial
	streamEvent := map[string]any{
		"type": "stream_event",
		"event": map[string]any{
			"delta": map[string]any{"text": "fake delta"},
		},
	}
	_ = json.NewEncoder(os.Stdout).Encode(streamEvent)
	os.Stdout.Sync()

	// 2. assistant text block
	assistant := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": "fake response"},
			},
		},
	}
	_ = json.NewEncoder(os.Stdout).Encode(assistant)
	os.Stdout.Sync()

	// 3. result
	result := map[string]any{
		"type":           "result",
		"subtype":        "success",
		"total_cost_usd": 0.0001,
		"duration_ms":    42,
		"usage": map[string]int{
			"input_tokens":  10,
			"output_tokens": 5,
		},
		"session_id": sessionID,
	}

	// Emit permission denial if configured and tool not already allowed
	if denyTool != "" && !allowedSet[denyTool] {
		result["permission_denials"] = []map[string]any{
			{
				"tool_name":   denyTool,
				"tool_use_id": "toolu_fake_001",
				"tool_input": map[string]any{
					"file_path": "/tmp/fake.txt",
					"content":   "fake content",
				},
			},
		}
	}

	// FAKE_CLAUDE_QUESTIONS=N emits N AskUserQuestion denials in one result.
	// Only on first invocation (no --resume flag) to avoid infinite loops.
	hasResume := false
	for _, arg := range os.Args {
		if arg == "--resume" {
			hasResume = true
			break
		}
	}
	if qCountStr := os.Getenv("FAKE_CLAUDE_QUESTIONS"); qCountStr != "" && !hasResume {
		qCount, _ := strconv.Atoi(qCountStr)
		var denials []map[string]any
		for i := 0; i < qCount; i++ {
			denials = append(denials, map[string]any{
				"tool_name":   "AskUserQuestion",
				"tool_use_id": fmt.Sprintf("toolu_q_%03d", i),
				"tool_input": map[string]any{
					"questions": []map[string]any{
						{
							"question": fmt.Sprintf("Question %d?", i+1),
							"options": []map[string]any{
								{"label": fmt.Sprintf("Option %dA", i+1)},
								{"label": fmt.Sprintf("Option %dB", i+1)},
							},
						},
					},
				},
			})
		}
		result["permission_denials"] = denials
	}

	_ = json.NewEncoder(os.Stdout).Encode(result)
	os.Stdout.Sync()
}
