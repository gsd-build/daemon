// Unit tests for the pi to Claude stream-json translator.
//
// The sample event log exercises the stream shapes the browser reducer uses.
package pi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func loadPiSample(t *testing.T) [][]byte {
	t.Helper()
	path := filepath.Join("testdata", "pi-sample.ndjson")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}
	var lines [][]byte
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		l := scanner.Bytes()
		if len(l) == 0 {
			continue
		}
		dup := make([]byte, len(l))
		copy(dup, l)
		lines = append(lines, dup)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan sample: %v", err)
	}
	return lines
}

func TestTranslator_AllBrowserDispatchPoints(t *testing.T) {
	lines := loadPiSample(t)
	if len(lines) == 0 {
		t.Fatal("empty sample")
	}

	state := &translatorState{}
	var translated []rawEvent
	for _, l := range lines {
		out := translatePiEvent(l, state)
		translated = append(translated, out...)
	}

	// Tally dispatched events by what the browser cares about.
	type counts struct {
		systemInit             int
		streamEvent            int
		contentBlockStartText  int
		contentBlockStartTool  int
		contentBlockDeltaText  int
		contentBlockDeltaToolJ int
		contentBlockStop       int
		toolResult             int
	}
	var c counts

	for _, ev := range translated {
		var top struct {
			Type    string          `json:"type"`
			Subtype string          `json:"subtype"`
			Event   json.RawMessage `json:"event"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
				} `json:"content"`
			} `json:"message"`
		}
		_ = json.Unmarshal(ev.Raw, &top)

		switch top.Type {
		case "system":
			if top.Subtype == "init" {
				c.systemInit++
			}
		case "stream_event":
			c.streamEvent++
			var inner struct {
				Type         string `json:"type"`
				ContentBlock struct {
					Type string `json:"type"`
				} `json:"content_block"`
				Delta struct {
					Type string `json:"type"`
				} `json:"delta"`
			}
			_ = json.Unmarshal(top.Event, &inner)
			switch inner.Type {
			case "content_block_start":
				switch inner.ContentBlock.Type {
				case "text":
					c.contentBlockStartText++
				case "tool_use":
					c.contentBlockStartTool++
				}
			case "content_block_delta":
				switch inner.Delta.Type {
				case "text_delta":
					c.contentBlockDeltaText++
				case "input_json_delta":
					c.contentBlockDeltaToolJ++
				}
			case "content_block_stop":
				c.contentBlockStop++
			}
		case "user":
			for _, b := range top.Message.Content {
				if b.Type == "tool_result" {
					c.toolResult++
				}
			}
		}
	}

	t.Logf("translated %d events from %d pi lines", len(translated), len(lines))
	t.Logf("counts: system_init=%d stream_event=%d", c.systemInit, c.streamEvent)
	t.Logf("  content_block_start: text=%d tool_use=%d", c.contentBlockStartText, c.contentBlockStartTool)
	t.Logf("  content_block_delta: text=%d input_json=%d", c.contentBlockDeltaText, c.contentBlockDeltaToolJ)
	t.Logf("  content_block_stop: %d", c.contentBlockStop)
	t.Logf("  tool_result: %d", c.toolResult)

	if c.systemInit != 1 {
		t.Errorf("expected 1 system init, got %d", c.systemInit)
	}
	if c.contentBlockStartTool < 1 {
		t.Errorf("expected at least 1 tool_use content_block_start, got %d", c.contentBlockStartTool)
	}
	if c.contentBlockDeltaToolJ < 1 {
		t.Errorf("expected at least 1 input_json_delta (tool args streaming), got %d",
			c.contentBlockDeltaToolJ)
	}
	if c.contentBlockStop < 1 {
		t.Errorf("expected at least 1 content_block_stop, got %d", c.contentBlockStop)
	}
	if c.toolResult != 1 {
		t.Errorf("expected exactly 1 tool_result, got %d", c.toolResult)
	}
	// This sample has no streaming text response
	// (the model called Bash and the answer came at agent_end without a
	// message_update text stream). text_start/delta/end paths are exercised
	// by focused shape tests.
}

// Spot-check the wire shape of a translated text_delta to make sure it matches
// what the browser's reducer expects exactly: it dispatches on
//
//	inner.type === "content_block_delta" && inner.delta.type === "text_delta"
//	&& inner.delta.text
func TestTranslator_TextDeltaShape(t *testing.T) {
	state := &translatorState{sessionID: "sess-x"}
	piMu := []byte(`{
	  "type": "message_update",
	  "assistantMessageEvent": {
	    "type": "text_delta",
	    "contentIndex": 0,
	    "delta": "hello",
	    "partial": {"role":"assistant","content":[{"type":"text","text":"hello"}]}
	  }
	}`)
	out := translatePiEvent(piMu, state)
	if len(out) != 1 {
		t.Fatalf("expected 1 event, got %d", len(out))
	}
	var top struct {
		Type  string `json:"type"`
		Event struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		} `json:"event"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(out[0].Raw, &top); err != nil {
		t.Fatal(err)
	}
	if top.Type != "stream_event" {
		t.Errorf("type=%q want stream_event", top.Type)
	}
	if top.Event.Type != "content_block_delta" {
		t.Errorf("event.type=%q want content_block_delta", top.Event.Type)
	}
	if top.Event.Delta.Type != "text_delta" {
		t.Errorf("event.delta.type=%q want text_delta", top.Event.Delta.Type)
	}
	if top.Event.Delta.Text != "hello" {
		t.Errorf("event.delta.text=%q want hello", top.Event.Delta.Text)
	}
	if top.SessionID != "sess-x" {
		t.Errorf("session_id=%q want sess-x", top.SessionID)
	}
}

// Spot-check the tool_use start carries id + name pulled from partial snapshot.
func TestTranslator_ToolCallStartShape(t *testing.T) {
	state := &translatorState{}
	piMu := []byte(`{
	  "type": "message_update",
	  "assistantMessageEvent": {
	    "type": "toolcall_start",
	    "contentIndex": 0,
	    "partial": {
	      "role": "assistant",
	      "content": [
	        {"type":"toolCall","id":"toolu_abc","name":"bash","arguments":{}}
	      ]
	    }
	  }
	}`)
	out := translatePiEvent(piMu, state)
	if len(out) != 1 {
		t.Fatalf("expected 1 event, got %d", len(out))
	}
	var top struct {
		Event struct {
			Type         string `json:"type"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
		} `json:"event"`
	}
	if err := json.Unmarshal(out[0].Raw, &top); err != nil {
		t.Fatal(err)
	}
	if top.Event.Type != "content_block_start" {
		t.Errorf("event.type=%q want content_block_start", top.Event.Type)
	}
	if top.Event.ContentBlock.Type != "tool_use" {
		t.Errorf("content_block.type=%q want tool_use", top.Event.ContentBlock.Type)
	}
	if top.Event.ContentBlock.ID != "toolu_abc" {
		t.Errorf("content_block.id=%q want toolu_abc", top.Event.ContentBlock.ID)
	}
	if top.Event.ContentBlock.Name != "bash" {
		t.Errorf("content_block.name=%q want bash", top.Event.ContentBlock.Name)
	}
}

// Spot-check tool_execution_end to user{tool_result} synthesis.
func TestTranslator_ToolResultShape(t *testing.T) {
	state := &translatorState{}
	piEnd := []byte(`{
	  "type": "tool_execution_end",
	  "toolCallId": "toolu_abc",
	  "toolName": "bash",
	  "result": {"content":[{"type":"text","text":"hello world"}]},
	  "isError": false
	}`)
	out := translatePiEvent(piEnd, state)
	if len(out) != 1 {
		t.Fatalf("expected 1 event, got %d", len(out))
	}
	var top struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type      string `json:"type"`
				ToolUseID string `json:"tool_use_id"`
				Content   string `json:"content"`
				IsError   bool   `json:"is_error"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(out[0].Raw, &top); err != nil {
		t.Fatal(err)
	}
	if top.Type != "user" {
		t.Errorf("type=%q want user", top.Type)
	}
	if len(top.Message.Content) != 1 {
		t.Fatalf("content len=%d want 1", len(top.Message.Content))
	}
	c := top.Message.Content[0]
	if c.Type != "tool_result" {
		t.Errorf("content[0].type=%q want tool_result", c.Type)
	}
	if c.ToolUseID != "toolu_abc" {
		t.Errorf("tool_use_id=%q want toolu_abc", c.ToolUseID)
	}
	if c.Content != "hello world" {
		t.Errorf("content=%q want 'hello world'", c.Content)
	}
}

func TestTranslator_AgentEndResultShape(t *testing.T) {
	raw := []byte(`{
	  "type":"agent_end",
	  "messages":[{
	    "role":"assistant",
	    "usage":{
	      "input":12,
	      "output":7,
	      "cacheRead":3,
	      "cacheWrite":2,
	      "cost":{"total":0.0042}
	    }
	  }]
	}`)

	eventRaw, err := synthesizeResultEvent(raw, "sess-result", 1500)
	if err != nil {
		t.Fatal(err)
	}

	var got struct {
		Type         string  `json:"type"`
		SessionID    string  `json:"session_id"`
		TotalCostUSD float64 `json:"total_cost_usd"`
		DurationMs   int     `json:"duration_ms"`
		Usage        struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			CacheReadInput     int `json:"cache_read_input_tokens"`
			CacheCreationInput int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(eventRaw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "result" {
		t.Fatalf("result type=%q", got.Type)
	}
	if got.SessionID != "sess-result" {
		t.Fatalf("session_id=%q", got.SessionID)
	}
	if got.TotalCostUSD != 0.0042 {
		t.Fatalf("total_cost_usd=%f", got.TotalCostUSD)
	}
	if got.DurationMs != 1500 {
		t.Fatalf("duration_ms=%d", got.DurationMs)
	}
	if got.Usage.InputTokens != 12 || got.Usage.OutputTokens != 7 {
		t.Fatalf("usage tokens=%+v", got.Usage)
	}
	if got.Usage.CacheReadInput != 3 || got.Usage.CacheCreationInput != 2 {
		t.Fatalf("cache usage=%+v", got.Usage)
	}
}
