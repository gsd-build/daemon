package display

import (
	"encoding/json"
	"fmt"
	"io"
)

// StreamHandler processes stream_event JSON for progressive text rendering.
type StreamHandler struct {
	w        io.Writer
	level    VerbosityLevel
	active   bool
	skipNext bool
}

// NewStreamHandler creates a handler that writes streaming text to w.
func NewStreamHandler(w io.Writer, level VerbosityLevel) *StreamHandler {
	return &StreamHandler{w: w, level: level}
}

// IsStreamEvent returns true if the raw JSON is a stream_event.
func IsStreamEvent(raw []byte) bool {
	var peek struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &peek) != nil {
		return false
	}
	return peek.Type == "stream_event"
}

// Handle processes a stream_event JSON line. Returns true if it was handled.
func (sh *StreamHandler) Handle(raw []byte) bool {
	if !IsStreamEvent(raw) {
		return false
	}

	var ev struct {
		Event struct {
			Type         string `json:"type"`
			ContentBlock struct {
				Type string `json:"type"`
			} `json:"content_block"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		} `json:"event"`
	}
	if json.Unmarshal(raw, &ev) != nil {
		return true
	}

	switch ev.Event.Type {
	case "content_block_start":
		if ev.Event.ContentBlock.Type == "text" {
			sh.active = true
			sh.skipNext = false
			if sh.level != Quiet {
				fmt.Fprint(sh.w, "\n")
			}
		}

	case "content_block_delta":
		if sh.active && ev.Event.Delta.Type == "text_delta" && sh.level != Quiet {
			fmt.Fprint(sh.w, ev.Event.Delta.Text)
		}

	case "content_block_stop":
		if sh.active {
			if sh.level != Quiet {
				fmt.Fprint(sh.w, "\n")
			}
			sh.active = false
			sh.skipNext = true
		}
	}

	return true
}

// ShouldSkipText returns true if assistant text blocks should be skipped
// because they were already rendered via streaming deltas.
func (sh *StreamHandler) ShouldSkipText() bool {
	return sh.skipNext || sh.active
}

// ConsumeSkip resets the skip flag after it has been checked.
func (sh *StreamHandler) ConsumeSkip() {
	sh.skipNext = false
}
