package wal

import (
	"path/filepath"
	"testing"
)

func TestAppendAndReadAll(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(filepath.Join(dir, "sess-1.jsonl"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	if err := w.Append(1, []byte(`{"type":"stream","event":"first"}`)); err != nil {
		t.Fatalf("append 1: %v", err)
	}
	if err := w.Append(2, []byte(`{"type":"stream","event":"second"}`)); err != nil {
		t.Fatalf("append 2: %v", err)
	}
	if err := w.Append(3, []byte(`{"type":"stream","event":"third"}`)); err != nil {
		t.Fatalf("append 3: %v", err)
	}

	entries, err := w.ReadFrom(0)
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].Seq != 1 || entries[2].Seq != 3 {
		t.Errorf("unexpected sequence numbers: %+v", entries)
	}
}

func TestReadFromCursor(t *testing.T) {
	dir := t.TempDir()
	w, _ := Open(filepath.Join(dir, "sess.jsonl"))
	defer w.Close()

	for i := int64(1); i <= 5; i++ {
		_ = w.Append(i, []byte(`{"n":1}`))
	}

	entries, err := w.ReadFrom(2)
	if err != nil {
		t.Fatal(err)
	}
	// Should return seq > 2
	if len(entries) != 3 {
		t.Fatalf("expected 3, got %d", len(entries))
	}
	if entries[0].Seq != 3 {
		t.Errorf("expected first entry seq=3, got %d", entries[0].Seq)
	}
}

func TestPruneUpTo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess.jsonl")
	w, _ := Open(path)
	for i := int64(1); i <= 10; i++ {
		_ = w.Append(i, []byte(`{"x":1}`))
	}
	w.Close()

	// Reopen and prune
	w2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()
	if err := w2.PruneUpTo(5); err != nil {
		t.Fatalf("prune: %v", err)
	}

	entries, _ := w2.ReadFrom(0)
	if len(entries) != 5 {
		t.Fatalf("expected 5 entries after prune, got %d", len(entries))
	}
	if entries[0].Seq != 6 {
		t.Errorf("expected first entry seq=6, got %d", entries[0].Seq)
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess.jsonl")

	w, _ := Open(path)
	_ = w.Append(1, []byte(`"a"`))
	_ = w.Append(2, []byte(`"b"`))
	w.Close()

	w2, _ := Open(path)
	defer w2.Close()
	entries, _ := w2.ReadFrom(0)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after reopen, got %d", len(entries))
	}
}
