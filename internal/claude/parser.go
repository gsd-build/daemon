// Package claude handles spawning and communicating with `claude -p`.
package claude

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// Event is a single Claude stream-json output event.
// The Type field is extracted for dispatching; Raw contains the full JSON
// so we can forward it to the relay unchanged.
type Event struct {
	Type string          `json:"type"`
	Raw  json.RawMessage `json:"-"`
}

// Parse reads NDJSON from r and invokes onEvent for each line.
func Parse(r io.Reader, onEvent func(Event) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		raw := make([]byte, len(line))
		copy(raw, line)

		var peek struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &peek); err != nil {
			// Skip malformed lines
			continue
		}

		if err := onEvent(Event{Type: peek.Type, Raw: raw}); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan: %w", err)
	}
	return nil
}
