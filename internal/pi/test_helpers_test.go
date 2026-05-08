package pi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFakePiNamed(t *testing.T, name string, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	script := "#!/usr/bin/env bash\nset -euo pipefail\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
	return path
}

func readNulArgsFile(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read args file: %v", err)
	}
	args := strings.Split(string(data), "\x00")
	if len(args) > 0 && args[len(args)-1] == "" {
		args = args[:len(args)-1]
	}
	return args
}

func argIndex(args []string, want string) int {
	for i, arg := range args {
		if arg == want {
			return i
		}
	}
	return -1
}

func argValue(t *testing.T, args []string, flag string) string {
	t.Helper()
	index := argIndex(args, flag)
	if index < 0 {
		t.Fatalf("args missing %s: %v", flag, args)
	}
	if index+1 >= len(args) {
		t.Fatalf("args missing value for %s: %v", flag, args)
	}
	return args[index+1]
}

func assertArgValue(t *testing.T, args []string, flag string, want string) {
	t.Helper()
	if got := argValue(t, args, flag); got != want {
		t.Fatalf("%s = %q, want %q; args=%v", flag, got, want, args)
	}
}

func readJSONLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read jsonl file: %v", err)
	}
	var frames []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("parse json line %q: %v", line, err)
		}
		frames = append(frames, frame)
	}
	return frames
}
