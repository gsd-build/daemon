package terminal

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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

func (c *captureEvents) hasExitReason(reason string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, got := range c.exits {
		if got == reason {
			return true
		}
	}
	return false
}

func (c *captureEvents) exitReasons() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]string(nil), c.exits...)
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

func TestManagerCloseAllStopsStubbornShell(t *testing.T) {
	shellPath := filepath.Join(t.TempDir(), "stubborn-shell")
	script := "#!/bin/sh\ntrap '' TERM\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(shellPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write shell: %v", err)
	}
	t.Setenv("SHELL", shellPath)

	events := &captureEvents{}
	m := NewManager(events, Limits{
		ScrollbackBytes:        1024,
		OutputChunkSize:        1024,
		TerminationGracePeriod: 50 * time.Millisecond,
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

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	m.CloseAll(ctx, ReasonDaemonShutdown)
	if elapsed := time.Since(start); elapsed > 750*time.Millisecond {
		t.Fatalf("CloseAll took %s, want bounded shutdown", elapsed)
	}
	if _, ok := m.get(req.TerminalID); ok {
		t.Fatal("terminal session still registered after CloseAll")
	}
	if !events.hasExitReason(ReasonDaemonShutdown) {
		t.Fatalf("exit reasons = %v, want %q", events.exitReasons(), ReasonDaemonShutdown)
	}
}

func TestManagerEnforcesMaxSessions(t *testing.T) {
	events := &captureEvents{}
	m := NewManager(events, Limits{
		ScrollbackBytes: 1024,
		MaxSessions:     1,
		OutputChunkSize: 1024,
	})
	defer m.CloseAll(context.Background(), ReasonDaemonShutdown)

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
		t.Fatalf("Open first: %v", err)
	}
	req.RequestID = "open-2"
	req.TerminalID = "term-2"
	if err := m.Open(context.Background(), req); err == nil {
		t.Fatal("second Open succeeded, want max session error")
	}
}

func TestManagerClosesIdleTerminal(t *testing.T) {
	events := &captureEvents{}
	m := NewManager(events, Limits{
		ScrollbackBytes:        1024,
		IdleTimeout:            50 * time.Millisecond,
		MaxLifetime:            time.Second,
		OutputChunkSize:        1024,
		TerminationGracePeriod: 50 * time.Millisecond,
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
	deadline := time.After(2 * time.Second)
	for !events.hasExitReason(ReasonDisconnectTimeout) {
		select {
		case <-deadline:
			t.Fatalf("exit reasons = %v, want %q", events.exitReasons(), ReasonDisconnectTimeout)
		case <-time.After(20 * time.Millisecond):
		}
	}
}
