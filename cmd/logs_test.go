package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStreamLogFileFollowsExistingAndAppendedContent(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "daemon.log")
	if err := os.WriteFile(logPath, []byte("line 1\n"), 0600); err != nil {
		t.Fatalf("write initial log: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var out bytes.Buffer
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
