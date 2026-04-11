package pidfile

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestWriteAndRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.pid")

	if err := Write(path, 12345); err != nil {
		t.Fatalf("write: %v", err)
	}

	pid, err := Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if pid != 12345 {
		t.Errorf("expected 12345, got %d", pid)
	}
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.pid")

	if err := Write(path, 99); err != nil {
		t.Fatal(err)
	}
	Remove(path)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected file to be removed")
	}
}

func TestReadMissingFile(t *testing.T) {
	_, err := Read("/nonexistent/path/test.pid")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestCleanStale(t *testing.T) {
	dir := t.TempDir()

	// Write a PID file with PID 0 (guaranteed not running)
	path := filepath.Join(dir, "fake.pid")
	if err := os.WriteFile(path, []byte("0"), 0600); err != nil {
		t.Fatal(err)
	}

	// Write a PID file with our own PID (guaranteed running)
	selfPath := filepath.Join(dir, "self.pid")
	if err := os.WriteFile(selfPath, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}

	cleaned := CleanStale(dir)

	// PID 0 file should be removed
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected stale PID file to be removed")
	}

	// Our own PID file should remain
	if _, err := os.Stat(selfPath); err != nil {
		t.Error("expected self PID file to remain")
	}

	if cleaned != 1 {
		t.Errorf("expected 1 cleaned, got %d", cleaned)
	}
}

func TestPidDir(t *testing.T) {
	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if dir == "" {
		t.Error("expected non-empty dir")
	}
}
