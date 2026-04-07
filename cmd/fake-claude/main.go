// fake-claude is a test helper that mimics `claude -p --input-format stream-json
// --output-format stream-json`. It reads NDJSON from stdin and emits scripted
// responses based on the content.
//
// Usage: go run ./cmd/fake-claude  (during tests)
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		var msg map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}

		// Echo back an assistant event + a result event
		assistant := map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "fake response"},
				},
			},
		}
		_ = json.NewEncoder(os.Stdout).Encode(assistant)

		result := map[string]any{
			"type":           "result",
			"total_cost_usd": 0.0001,
			"duration_ms":    42,
			"usage": map[string]int{
				"input_tokens":  10,
				"output_tokens": 5,
			},
			"session_id": "fake-session-123",
		}
		_ = json.NewEncoder(os.Stdout).Encode(result)
		os.Stdout.Sync()

		// Simulate work
		time.Sleep(10 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "fake-claude: stdin closed, exiting")
}
