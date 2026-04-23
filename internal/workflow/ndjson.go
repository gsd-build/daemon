package workflow

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
)

// StreamEvent is a parsed NDJSON event from claude -p.
type StreamEvent struct {
	Type string
	Raw  json.RawMessage
}

// NDJSONScanner wraps a bufio.Scanner to parse stream-json events.
type NDJSONScanner struct {
	scanner *bufio.Scanner
	event   StreamEvent
}

// NewNDJSONScanner creates a scanner for claude stream-json output.
func NewNDJSONScanner(r io.Reader) *NDJSONScanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64*1024), 4*1024*1024)
	return &NDJSONScanner{scanner: s}
}

// Scan reads the next event. It returns false at EOF or scanner error.
func (n *NDJSONScanner) Scan() bool {
	for n.scanner.Scan() {
		line := n.scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		raw := make(json.RawMessage, len(line))
		copy(raw, line)

		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(raw, &envelope); err != nil {
			slog.Debug("ndjson: skip malformed line", "err", err)
			continue
		}

		typ := stringField(envelope, "type")
		if typ == "stream_event" || typ == "rate_limit_event" {
			inner, ok := envelope["event"]
			if !ok {
				continue
			}
			raw = make(json.RawMessage, len(inner))
			copy(raw, inner)
			envelope = map[string]json.RawMessage{}
			if err := json.Unmarshal(raw, &envelope); err != nil {
				slog.Debug("ndjson: skip malformed event envelope", "err", err)
				continue
			}
			typ = stringField(envelope, "type")
		}

		if typ == "" || typ == "system" {
			continue
		}
		n.event = StreamEvent{Type: typ, Raw: raw}
		return true
	}
	if err := n.scanner.Err(); err != nil {
		slog.Debug("ndjson: scan failed", "err", err)
	}
	return false
}

// Event returns the last parsed event.
func (n *NDJSONScanner) Event() StreamEvent {
	return n.event
}

func stringField(fields map[string]json.RawMessage, name string) string {
	raw, ok := fields[name]
	if !ok {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}
