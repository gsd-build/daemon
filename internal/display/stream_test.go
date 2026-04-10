package display

import (
	"bytes"
	"testing"
)

func TestIsStreamEvent(t *testing.T) {
	if !IsStreamEvent([]byte(`{"type":"stream_event","event":{}}`)) {
		t.Error("should detect stream_event")
	}
	if IsStreamEvent([]byte(`{"type":"assistant"}`)) {
		t.Error("should not detect non-stream_event")
	}
}

func TestStreamHandlerTextDeltas(t *testing.T) {
	var buf bytes.Buffer
	sh := NewStreamHandler(&buf, Default)

	sh.Handle([]byte(`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"text"}}}`))
	sh.Handle([]byte(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello "}}}`))
	sh.Handle([]byte(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"world"}}}`))
	sh.Handle([]byte(`{"type":"stream_event","event":{"type":"content_block_stop"}}`))

	got := buf.String()
	if got != "\nHello world\n" {
		t.Errorf("expected '\\nHello world\\n', got %q", got)
	}
}

func TestStreamHandlerQuietSuppresses(t *testing.T) {
	var buf bytes.Buffer
	sh := NewStreamHandler(&buf, Quiet)

	sh.Handle([]byte(`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"text"}}}`))
	sh.Handle([]byte(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}}`))
	sh.Handle([]byte(`{"type":"stream_event","event":{"type":"content_block_stop"}}`))

	if buf.Len() != 0 {
		t.Errorf("quiet mode should suppress all output, got %q", buf.String())
	}
}

func TestStreamHandlerShouldSkipText(t *testing.T) {
	var buf bytes.Buffer
	sh := NewStreamHandler(&buf, Default)

	if sh.ShouldSkipText() {
		t.Error("should not skip before any streaming")
	}

	sh.Handle([]byte(`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"text"}}}`))
	if !sh.ShouldSkipText() {
		t.Error("should skip during active streaming")
	}

	sh.Handle([]byte(`{"type":"stream_event","event":{"type":"content_block_stop"}}`))
	if !sh.ShouldSkipText() {
		t.Error("should skip after streaming (before consume)")
	}

	sh.ConsumeSkip()
	if sh.ShouldSkipText() {
		t.Error("should not skip after consume")
	}
}

func TestStreamHandlerIgnoresNonTextBlocks(t *testing.T) {
	var buf bytes.Buffer
	sh := NewStreamHandler(&buf, Default)

	sh.Handle([]byte(`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"thinking"}}}`))
	if sh.ShouldSkipText() {
		t.Error("thinking blocks should not activate streaming")
	}
	if buf.Len() != 0 {
		t.Errorf("thinking block should not produce output, got %q", buf.String())
	}
}

func TestStreamHandlerReturnsFalseForNonStreamEvents(t *testing.T) {
	var buf bytes.Buffer
	sh := NewStreamHandler(&buf, Default)

	handled := sh.Handle([]byte(`{"type":"assistant","message":{}}`))
	if handled {
		t.Error("should return false for non-stream events")
	}
}
