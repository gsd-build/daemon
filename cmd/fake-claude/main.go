// fake-claude is a test helper that mimics `claude -p --input-format stream-json
// --output-format stream-json`. It reads NDJSON from stdin and emits scripted
// responses based on the content and on environment variables.
//
// Env vars:
//
//	FAKE_CLAUDE_DENY_TOOL=<name>  — emit a permission_denials for the first turn
//	                                (or any turn where the tool is not in --allowedTools)
//	FAKE_CLAUDE_SESSION_ID=<id>   — override the synthetic session id
//	FAKE_CLAUDE_ARGS_FILE=<path>  — write os.Args[1:] as JSON to this file (preserved from Task 9)
//	FAKE_CLAUDE_STDERR=<text>     — write this string (plus newline) to stderr at startup
//	FAKE_CLAUDE_EXIT_CODE=<n>     — exit with this code immediately after writing stderr,
//	                                before reading stdin (used to test stderr capture)
//
// Usage: invoked by the daemon as `claude -p ...` during tests.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func main() {
	// If FAKE_CLAUDE_ARGS_FILE is set, write argv as JSON to that file.
	// This lets executor tests inspect the flags passed by the daemon
	// without polluting the stdout stream that the parser reads.
	if argsFile := os.Getenv("FAKE_CLAUDE_ARGS_FILE"); argsFile != "" {
		data, _ := json.Marshal(os.Args[1:])
		_ = os.WriteFile(argsFile, data, 0o600)
	}

	// Test hooks for stderr capture: emit an optional line to stderr,
	// and optionally force an immediate non-zero exit. When FAKE_CLAUDE_EXIT_CODE
	// is set, we bail before touching stdin so the test deterministically
	// sees a crashed subprocess.
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

	// Detect whether the daemon spawned us with --allowedTools (which means
	// the user has approved a tool). If the deny tool is in the allowed list,
	// suppress the deny.
	allowedSet := make(map[string]bool)
	for i, arg := range os.Args {
		if arg == "--allowedTools" && i+1 < len(os.Args) {
			for _, t := range strings.Split(os.Args[i+1], ",") {
				allowedSet[strings.TrimSpace(t)] = true
			}
		}
	}

	turnNum := 0
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		var msg map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		turnNum++

		// Emit a tiny stream_event partial (relay will skip persisting it)
		streamEvent := map[string]any{
			"type": "stream_event",
			"event": map[string]any{
				"delta": map[string]any{"text": "fake delta"},
			},
		}
		_ = json.NewEncoder(os.Stdout).Encode(streamEvent)

		// Emit an assistant text block
		assistant := map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "fake response"},
				},
			},
		}
		_ = json.NewEncoder(os.Stdout).Encode(assistant)

		// Build result
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

		// On the first turn, emit a permission_denial unless the tool is now allowed
		if turnNum == 1 && denyTool != "" && !allowedSet[denyTool] {
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

		_ = json.NewEncoder(os.Stdout).Encode(result)
		os.Stdout.Sync()
	}
	fmt.Fprintln(os.Stderr, "fake-claude: stdin closed, exiting")
}
