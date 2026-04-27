package terminal

import (
	"bytes"
	"context"
	"testing"
	"time"
)

type captureEvents struct {
	opened int
	output bytes.Buffer
	exits  []string
	errors []string
}

func (c *captureEvents) SendTerminalOpened(OpenRequest, string, string, time.Time) error {
	c.opened++
	return nil
}
func (c *captureEvents) SendTerminalOutput(_ string, _ string, _ string, _ int64, data []byte) error {
	_, _ = c.output.Write(data)
	return nil
}
func (c *captureEvents) SendTerminalSnapshot(_ string, _ string, _ string, _ int64, data []byte) error {
	_, _ = c.output.Write(data)
	return nil
}
func (c *captureEvents) SendTerminalExit(_ string, _ string, _ string, reason string, _ int, _ string, _ time.Time) error {
	c.exits = append(c.exits, reason)
	return nil
}
func (c *captureEvents) SendTerminalError(_, _, _, _, msg string) error {
	c.errors = append(c.errors, msg)
	return nil
}

func TestManagerOpenWriteAndClose(t *testing.T) {
	events := &captureEvents{}
	m := NewManager(events, Limits{
		ScrollbackBytes: 1024,
		IdleTimeout:     time.Second,
		MaxLifetime:     time.Minute,
		OutputChunkSize: 1024,
		OutputFlush:     time.Millisecond,
	})

	req := OpenRequest{
		RequestID:  "open-1",
		TerminalID: "term-1",
		SessionID:  "sess-1",
		ChannelID:  "chan-1",
		CWD:        t.TempDir(),
		Cols:       80,
		Rows:       24,
	}
	if err := m.Open(context.Background(), req); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := m.Input("term-1", []byte("printf gsd-terminal-ok\nexit\n")); err != nil {
		t.Fatalf("Input: %v", err)
	}
	deadline := time.After(5 * time.Second)
	for !bytes.Contains(events.output.Bytes(), []byte("gsd-terminal-ok")) {
		select {
		case <-deadline:
			t.Fatalf("terminal output missing, got %q", events.output.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
	m.Close("term-1", ReasonClosedByUser)
}
