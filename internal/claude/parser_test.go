package claude

import (
	"bytes"
	"testing"
)

func TestParserEmitsEventsPerLine(t *testing.T) {
	input := bytes.NewReader([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}
{"type":"result","total_cost_usd":0.01,"duration_ms":100}
`))

	var events []Event
	err := Parse(input, func(e Event) error {
		events = append(events, e)
		return nil
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Type != "assistant" {
		t.Errorf("unexpected type: %s", events[0].Type)
	}
	if events[1].Type != "result" {
		t.Errorf("unexpected type: %s", events[1].Type)
	}
}

func TestParserSkipsEmptyLines(t *testing.T) {
	input := bytes.NewReader([]byte("\n\n{\"type\":\"ok\"}\n\n"))
	var count int
	_ = Parse(input, func(e Event) error {
		count++
		return nil
	})
	if count != 1 {
		t.Fatalf("expected 1 event, got %d", count)
	}
}

func TestParserHandlesLargeLine(t *testing.T) {
	big := make([]byte, 128*1024)
	for i := range big {
		big[i] = 'a'
	}
	input := bytes.NewReader([]byte(`{"type":"test","payload":"` + string(big) + `"}` + "\n"))
	var count int
	err := Parse(input, func(e Event) error {
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 event, got %d", count)
	}
}
