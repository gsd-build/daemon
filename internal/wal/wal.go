// Package wal provides a per-session append-only write-ahead log on disk.
// Every event sent to the relay is written here first; the relay ACKs back
// with a sequence number after persisting to Supabase, at which point the
// daemon can prune entries up to that sequence.
//
// Format: one JSON object per line, each of shape { "seq": int64, "data": <raw JSON> }.
package wal

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Entry is one record in the log.
type Entry struct {
	Seq  int64           `json:"seq"`
	Data json.RawMessage `json:"data"`
}

// Log is an open per-session WAL file.
type Log struct {
	mu   sync.Mutex
	path string
	f    *os.File
	w    *bufio.Writer
}

// Open returns a Log rooted at path, creating parent directories if needed.
// Appends go to the end of the file; ReadFrom scans from the start.
func Open(path string) (*Log, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return nil, fmt.Errorf("open wal: %w", err)
	}
	return &Log{
		path: path,
		f:    f,
		w:    bufio.NewWriter(f),
	}, nil
}

// Append writes a new entry and fsyncs.
func (l *Log) Append(seq int64, data []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry := Entry{Seq: seq, Data: data}
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal entry: %w", err)
	}
	line = append(line, '\n')

	if _, err := l.w.Write(line); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := l.w.Flush(); err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	if err := l.f.Sync(); err != nil {
		return fmt.Errorf("fsync: %w", err)
	}
	return nil
}

// ReadFrom returns all entries whose Seq > fromSeq.
func (l *Log) ReadFrom(fromSeq int64) ([]Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Flush any pending buffered writes
	if err := l.w.Flush(); err != nil {
		return nil, err
	}

	f, err := os.Open(l.path)
	if err != nil {
		return nil, fmt.Errorf("open for read: %w", err)
	}
	defer f.Close()

	var entries []Entry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("parse entry: %w", err)
		}
		if e.Seq > fromSeq {
			entries = append(entries, e)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	return entries, nil
}

// PruneUpTo rewrites the log with only entries where Seq > upTo.
func (l *Log) PruneUpTo(upTo int64) error {
	remaining, err := l.ReadFrom(upTo)
	if err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	tmp := l.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("open tmp: %w", err)
	}
	w := bufio.NewWriter(f)
	for _, e := range remaining {
		line, _ := json.Marshal(e)
		line = append(line, '\n')
		if _, err := w.Write(line); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	// Close current file, swap atomically, reopen
	_ = l.w.Flush()
	_ = l.f.Close()
	if err := os.Rename(tmp, l.path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	nf, err := os.OpenFile(l.path, os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("reopen: %w", err)
	}
	l.f = nf
	l.w = bufio.NewWriter(nf)
	return nil
}

// Close flushes and closes the file.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.w != nil {
		_ = l.w.Flush()
	}
	return l.f.Close()
}
