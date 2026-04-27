package terminal

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"
)

type captureEvents struct {
	mu     sync.Mutex
	opened int
	output bytes.Buffer
	exits  []string
	errors []string
}

func (c *captureEvents) SendTerminalOpened(OpenRequest, string, string, time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.opened++
	return nil
}
func (c *captureEvents) SendTerminalOutput(_ string, _ string, _ string, _ int64, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, _ = c.output.Write(data)
	return nil
}
func (c *captureEvents) SendTerminalSnapshot(_ string, _ string, _ string, _ int64, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, _ = c.output.Write(data)
	return nil
}
func (c *captureEvents) SendTerminalExit(_ string, _ string, _ string, reason string, _ int, _ string, _ time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.exits = append(c.exits, reason)
	return nil
}
func (c *captureEvents) SendTerminalError(_, _, _, _, msg string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.errors = append(c.errors, msg)
	return nil
}

func (c *captureEvents) outputContains(needle []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return bytes.Contains(c.output.Bytes(), needle)
}

func (c *captureEvents) outputString() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.output.String()
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
	for !events.outputContains([]byte("gsd-terminal-ok")) {
		select {
		case <-deadline:
			t.Fatalf("terminal output missing, got %q", events.outputString())
		case <-time.After(20 * time.Millisecond):
		}
	}
	m.Close("term-1", ReasonClosedByUser)
}
