package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.b = append(b.b, p...)
	return len(p), nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(append([]byte(nil), b.b...))
}

func TestStreamLogFileFollowsExistingAndAppendedContent(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "daemon.log")
	if err := os.WriteFile(logPath, []byte("line 1\n"), 0600); err != nil {
		t.Fatalf("write initial log: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var out lockedBuffer
	errCh := make(chan error, 1)
	go func() {
		errCh <- streamLogFile(ctx, logPath, &out, 10*time.Millisecond)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(out.String(), "line 1") {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for initial content, got %q", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	fh, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("open log for append: %v", err)
	}
	if _, err := fh.WriteString("line 2\n"); err != nil {
		t.Fatalf("append log: %v", err)
	}
	if err := fh.Close(); err != nil {
		t.Fatalf("close appended log: %v", err)
	}

	deadline = time.Now().Add(2 * time.Second)
	for !strings.Contains(out.String(), "line 2") {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for appended content, got %q", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("streamLogFile returned error: %v", err)
	}
}

func TestFilterLogLinesByTaskAndSession(t *testing.T) {
	lines := []string{
		`{"time":"2026-04-30T12:30:55Z","level":"INFO","event":"task_lifecycle","phase":"task_received","taskId":"task-1","sessionId":"session-1"}`,
		`{"time":"2026-04-30T12:30:56Z","level":"INFO","event":"task_lifecycle","phase":"task_received","taskId":"task-2","sessionId":"session-2"}`,
	}
	got := filterLogLines(lines, logFilter{TaskID: "task-1"})
	if len(got) != 1 || got[0].TaskID != "task-1" {
		t.Fatalf("filtered by task = %#v", got)
	}
	got = filterLogLines(lines, logFilter{SessionID: "session-2"})
	if len(got) != 1 || got[0].SessionID != "session-2" {
		t.Fatalf("filtered by session = %#v", got)
	}
}

func TestPrettyTimelineIncludesPromptPreviewAndFailureCode(t *testing.T) {
	events := []logEvent{
		{Time: "2026-04-30T12:30:55Z", Event: "task_lifecycle", Phase: "task_received", TaskID: "d1fae004-71f0-481a-9980-0cd6cecf49cb", SessionID: "session-1", PromptPreview: "write the full update spec"},
		{Time: "2026-04-30T12:32:26Z", Event: "task_lifecycle", Phase: "timed_out", FailureCode: "no_first_event_timeout", ElapsedMs: 90000},
	}
	got := renderPrettyTimeline(events, colorNever)
	if !strings.Contains(got, "task received") || !strings.Contains(got, `"write the full update spec"`) {
		t.Fatalf("missing received line: %s", got)
	}
	if !strings.Contains(got, "no_first_event_timeout") {
		t.Fatalf("missing failure code: %s", got)
	}
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("color escaped in colorNever output: %q", got)
	}
}
