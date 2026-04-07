package wal

import (
	"path/filepath"
	"testing"
)

func TestScanDirectoryReturnsHighestSequencePerSession(t *testing.T) {
	dir := t.TempDir()

	a, _ := Open(filepath.Join(dir, "session-aaa.jsonl"))
	for i := int64(1); i <= 5; i++ {
		_ = a.Append(i, []byte(`"x"`))
	}
	a.Close()

	b, _ := Open(filepath.Join(dir, "session-bbb.jsonl"))
	for i := int64(1); i <= 12; i++ {
		_ = b.Append(i, []byte(`"y"`))
	}
	b.Close()

	got, err := ScanDirectory(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got["session-aaa"] != 5 {
		t.Errorf("expected session-aaa=5, got %d", got["session-aaa"])
	}
	if got["session-bbb"] != 12 {
		t.Errorf("expected session-bbb=12, got %d", got["session-bbb"])
	}
}

func TestScanDirectoryHandlesMissingDir(t *testing.T) {
	got, err := ScanDirectory("/nonexistent/path/should/not/exist")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %+v", got)
	}
}
