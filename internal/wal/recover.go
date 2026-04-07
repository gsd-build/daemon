package wal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ScanDirectory walks walDir and returns a map of sessionID → highest sequence
// number found in each WAL file. Filenames are expected to be "<sessionId>.jsonl".
// A session with an empty or missing WAL file is omitted from the result.
func ScanDirectory(walDir string) (map[string]int64, error) {
	out := make(map[string]int64)

	entries, err := os.ReadDir(walDir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("read wal dir: %w", err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		sessionID := strings.TrimSuffix(name, ".jsonl")

		log, err := Open(filepath.Join(walDir, name))
		if err != nil {
			continue
		}
		walEntries, err := log.ReadFrom(0)
		_ = log.Close()
		if err != nil || len(walEntries) == 0 {
			continue
		}

		var maxSeq int64
		for _, entry := range walEntries {
			if entry.Seq > maxSeq {
				maxSeq = entry.Seq
			}
		}
		out[sessionID] = maxSeq
	}

	return out, nil
}
