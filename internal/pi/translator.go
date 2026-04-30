// Package pi translates pi NDJSON into Claude stream-json event shapes.
//
// The cloud relay stores and forwards stream events as opaque JSON. The
// browser reducer dispatches on Claude stream-json shapes, so translation
// happens at the daemon executor boundary.
package pi

import (
	"encoding/json"

	protocol "github.com/gsd-build/protocol-go"
)

// pi event shapes (only the fields we read).
type piMessageUpdate struct {
	Type                  string `json:"type"`
	AssistantMessageEvent struct {
		Type         string          `json:"type"`
		ContentIndex int             `json:"contentIndex"`
		Delta        string          `json:"delta"`   // text_delta / toolcall_delta
		Content      string          `json:"content"` // text_end final text
		Partial      json.RawMessage `json:"partial"`
	} `json:"assistantMessageEvent"`
}

type piToolStart struct {
	Type       string         `json:"type"`
	ToolCallID string         `json:"toolCallId"`
	ToolName   string         `json:"toolName"`
	Args       map[string]any `json:"args"`
}

type piToolEnd struct {
	Type       string `json:"type"`
	ToolCallID string `json:"toolCallId"`
	ToolName   string `json:"toolName"`
	Result     struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Details map[string]any `json:"details"`
	} `json:"result"`
	IsError bool `json:"isError"`
}

type piMessageEnd struct {
	Type    string `json:"type"`
	Message struct {
		Role    string `json:"role"`
		Content []struct {
			Type      string         `json:"type"`
			Text      string         `json:"text"`
			ID        string         `json:"id"`
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"content"`
	} `json:"message"`
}

type piSession struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	CWD  string `json:"cwd"`
}

// We need to read the partial.content[contentIndex] block to know whether a
// toolcall_start is for which toolCall (by index to id+name). Pi includes a
// snapshot of the running assistant message in every message_update.
type piPartialAssistant struct {
	Content []struct {
		Type      string         `json:"type"` // "text" | "toolCall"
		ID        string         `json:"id"`
		Name      string         `json:"name"`
		Text      string         `json:"text"`
		Arguments map[string]any `json:"arguments"`
	} `json:"content"`
}

// piToAnthropicToolName maps pi's tool vocabulary onto the Anthropic
// PascalCase names the web UI is built around. The mapping is name-only:
// it covers tools whose pi input shape already matches the Anthropic
// renderer's expected fields. Tools with divergent input shapes
// (read/write/edit, ls, ask_human, file_change) pass through unchanged so
// the UI can render them under their pi names with pi-shaped inputs.
//
// Includes codex --app-server synthetic tools (shell, file_change) so the
// same web UI works across providers without a second normalization layer.
func piToAnthropicToolName(name string) string {
	switch name {
	case "bash", "shell":
		return "Bash"
	case "find":
		return "Glob"
	case "grep":
		return "Grep"
	default:
		return name
	}
}

func NormalizeCompactReason(reason string) protocol.CompactReason {
	switch reason {
	case string(protocol.CompactReasonThreshold):
		return protocol.CompactReasonThreshold
	case string(protocol.CompactReasonOverflow):
		return protocol.CompactReasonOverflow
	default:
		return protocol.CompactReasonManual
	}
}

// translatePiEvent returns 0+ claude-shape Event values for one pi event.
// sessionID and modelID are threaded through so we can stamp them on
// emitted stream_event / assistant frames the browser may use.
func translatePiEvent(piRaw json.RawMessage, state *translatorState) []rawEvent {
	var peek struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(piRaw, &peek); err != nil {
		return nil
	}

	switch peek.Type {
	case "session":
		var s piSession
		_ = json.Unmarshal(piRaw, &s)
		state.sessionID = s.ID
		state.cwd = s.CWD
		return []rawEvent{makeSystemInit(s)}

	case "agent_start":
		if state.sessionID == "" {
			state.sessionID = "pi-rpc-" + state.taskID
			if state.sessionID == "pi-rpc-" {
				state.sessionID = "pi-rpc-session"
			}
			return []rawEvent{makeSystemInit(piSession{Type: "session", ID: state.sessionID, CWD: state.cwd})}
		}
		return nil

	case "turn_start", "turn_end", "message_start":
		// Lifecycle events with no claude analog the browser dispatches on.
		return nil

	case "message_end":
		var me piMessageEnd
		if err := json.Unmarshal(piRaw, &me); err != nil {
			return nil
		}
		if me.Message.Role != "assistant" {
			return nil
		}
		return []rawEvent{makeAssistantMessage(state, &me)}

	case "message_update":
		var mu piMessageUpdate
		if err := json.Unmarshal(piRaw, &mu); err != nil {
			return nil
		}
		return translateMessageUpdate(&mu, state)

	case "tool_execution_start":
		var ts piToolStart
		_ = json.Unmarshal(piRaw, &ts)
		state.lastToolCallID = ts.ToolCallID
		// content_block_stop for the tool_use that caused this. The browser uses
		// content_block_start/delta to render the tool call header; the actual
		// "tool is running" indication comes from absence of a tool_result.
		return []rawEvent{makeContentBlockStop(state)}

	case "tool_execution_end":
		var te piToolEnd
		_ = json.Unmarshal(piRaw, &te)
		// Synthesize a `user` message with a tool_result content block. This is
		// what the browser dispatches on for the result of a tool call.
		var resultText string
		for _, c := range te.Result.Content {
			if c.Type == "text" {
				resultText += c.Text
			}
		}
		var content any = resultText
		if te.ToolName == "subagent" && te.Result.Details != nil {
			details := make(map[string]any, len(te.Result.Details)+1)
			for k, v := range te.Result.Details {
				details[k] = v
			}
			if _, ok := details["finalText"]; !ok && resultText != "" {
				details["finalText"] = resultText
			}
			content = details
		}
		return []rawEvent{makeToolResultUser(state, te.ToolCallID, content, te.IsError)}

	case "agent_end":
		// Result event is synthesized separately by streamPiEvents (it needs
		// the same agentEnd raw bytes for usage rollup). Translator no-op.
		return nil

	default:
		return nil
	}
}

// translatorState carries cross-event data the translator needs.
type translatorState struct {
	sessionID string
	cwd       string
	model     string
	taskID    string // used for synthetic session ids in RPC mode

	// Track currently-active block in the assistant message so we can
	// emit content_block_start before content_block_delta for text.
	activeContentIdx  int
	activeContentType string // "text" | "tool_use" | ""

	// Last tool_call id surfaced for tool_execution_start translation.
	lastToolCallID string
}

// rawEvent is just a typed wrapper so callers know it's pre-marshaled NDJSON
// ready for forwarding through the existing pipeline.
type rawEvent struct {
	Type string
	Raw  json.RawMessage
}

type ChildTranslator struct {
	sessionID string
	state     translatorState
}

func NewChildTranslator(sessionID string, cwd string, model string) *ChildTranslator {
	return &ChildTranslator{
		sessionID: sessionID,
		state: translatorState{
			sessionID: sessionID,
			cwd:       cwd,
			model:     model,
			taskID:    sessionID,
		},
	}
}

func (t *ChildTranslator) Translate(raw json.RawMessage) []json.RawMessage {
	events := translatePiEvent(raw, &t.state)
	if len(events) == 0 {
		t.state.sessionID = t.sessionID
		return nil
	}
	out := make([]json.RawMessage, 0, len(events))
	for _, event := range events {
		out = append(out, stampSessionID(event.Raw, t.sessionID))
	}
	t.state.sessionID = t.sessionID
	return out
}

func stampSessionID(raw json.RawMessage, sessionID string) json.RawMessage {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw
	}
	obj["session_id"] = sessionID
	stamped, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return stamped
}

func makeSystemInit(s piSession) rawEvent {
	out, _ := json.Marshal(map[string]any{
		"type":       "system",
		"subtype":    "init",
		"session_id": s.ID,
		"cwd":        s.CWD,
	})
	return rawEvent{Type: "system", Raw: out}
}

// translateMessageUpdate emits stream_event{content_block_*} frames matching
// claude's stream-json shape. We emit content_block_start lazily (on the first
// delta of a new block) so the browser can dedup on content_block.id.
func translateMessageUpdate(mu *piMessageUpdate, state *translatorState) []rawEvent {
	switch mu.AssistantMessageEvent.Type {
	case "text_start":
		state.activeContentIdx = mu.AssistantMessageEvent.ContentIndex
		state.activeContentType = "text"
		out, _ := json.Marshal(map[string]any{
			"type": "stream_event",
			"event": map[string]any{
				"type":  "content_block_start",
				"index": mu.AssistantMessageEvent.ContentIndex,
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			},
			"session_id": state.sessionID,
		})
		return []rawEvent{{Type: "stream_event", Raw: out}}

	case "text_delta":
		out, _ := json.Marshal(map[string]any{
			"type": "stream_event",
			"event": map[string]any{
				"type":  "content_block_delta",
				"index": mu.AssistantMessageEvent.ContentIndex,
				"delta": map[string]any{
					"type": "text_delta",
					"text": mu.AssistantMessageEvent.Delta,
				},
			},
			"session_id": state.sessionID,
		})
		return []rawEvent{{Type: "stream_event", Raw: out}}

	case "text_end":
		out, _ := json.Marshal(map[string]any{
			"type": "stream_event",
			"event": map[string]any{
				"type":  "content_block_stop",
				"index": mu.AssistantMessageEvent.ContentIndex,
			},
			"session_id": state.sessionID,
		})
		return []rawEvent{{Type: "stream_event", Raw: out}}

	case "toolcall_start":
		// Pull the new toolCall block from partial.content[contentIndex]
		// so we know its id and name.
		var partial piPartialAssistant
		_ = json.Unmarshal(mu.AssistantMessageEvent.Partial, &partial)
		idx := mu.AssistantMessageEvent.ContentIndex
		if idx >= len(partial.Content) {
			return nil
		}
		blk := partial.Content[idx]
		if blk.Type != "toolCall" {
			return nil
		}
		state.activeContentIdx = idx
		state.activeContentType = "tool_use"
		out, _ := json.Marshal(map[string]any{
			"type": "stream_event",
			"event": map[string]any{
				"type":  "content_block_start",
				"index": idx,
				"content_block": map[string]any{
					"type": "tool_use",
					"id":   blk.ID,
					"name": piToAnthropicToolName(blk.Name),
				},
			},
			"session_id": state.sessionID,
		})
		return []rawEvent{{Type: "stream_event", Raw: out}}

	case "toolcall_delta":
		// Partial JSON deltas for tool args.
		out, _ := json.Marshal(map[string]any{
			"type": "stream_event",
			"event": map[string]any{
				"type":  "content_block_delta",
				"index": mu.AssistantMessageEvent.ContentIndex,
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": mu.AssistantMessageEvent.Delta,
				},
			},
			"session_id": state.sessionID,
		})
		return []rawEvent{{Type: "stream_event", Raw: out}}

	case "toolcall_end":
		// Pi may not emit this explicitly; tool_execution_start handles the
		// content_block_stop. Fire here too as a belt-and-braces measure.
		return []rawEvent{makeContentBlockStop(state)}
	}
	return nil
}

func makeContentBlockStop(state *translatorState) rawEvent {
	out, _ := json.Marshal(map[string]any{
		"type": "stream_event",
		"event": map[string]any{
			"type":  "content_block_stop",
			"index": state.activeContentIdx,
		},
		"session_id": state.sessionID,
	})
	return rawEvent{Type: "stream_event", Raw: out}
}

func makeToolResultUser(state *translatorState, toolUseID string, content any, isError bool) rawEvent {
	out, _ := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role": "user",
			"content": []map[string]any{
				{
					"type":        "tool_result",
					"tool_use_id": toolUseID,
					"content":     content,
					"is_error":    isError,
				},
			},
		},
		"session_id": state.sessionID,
	})
	return rawEvent{Type: "user", Raw: out}
}

func makeAssistantMessage(state *translatorState, me *piMessageEnd) rawEvent {
	content := make([]map[string]any, 0, len(me.Message.Content))
	for _, block := range me.Message.Content {
		switch block.Type {
		case "text":
			content = append(content, map[string]any{
				"type": "text",
				"text": block.Text,
			})
		case "toolCall":
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    block.ID,
				"name":  piToAnthropicToolName(block.Name),
				"input": block.Arguments,
			})
		}
	}

	out, _ := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"role":    "assistant",
			"content": content,
		},
		"session_id": state.sessionID,
	})
	return rawEvent{Type: "assistant", Raw: out}
}
