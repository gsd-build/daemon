// fake-claude-blockbuf is a second test-only fake of the claude CLI
// that replicates Node.js's libuv stream-layer buffering decision for
// process.stdout: line-buffered on a TTY, block-buffered on a pipe.
//
// REQUIRED: this fake MUST buffer by block when stdout is a pipe. Do
// not change it to always flush — the whole point of this variant is
// to catch regressions where the executor attaches a pipe (not a pty)
// to claude's stdout, in which case real Node-based claude goes
// silent for short responses and the daemon sees zero events.
//
// Why this fake is needed: Go's own os.Stdout is unbuffered (each
// Write goes straight to write(2)), which makes plain Go fakes
// unfaithful reproductions of real claude's pathological behavior.
// Node, by contrast, runs process.stdout.write() through libuv's
// stream abstraction, which holds short writes in a ~4–8 KiB
// userspace buffer and only flushes when the buffer fills, the
// process exits, or the stream detects the fd is a TTY. For a
// minimal assistant response (~200 bytes of stream-json), the
// buffer never fills, and there is no newline-triggered flush on a
// pipe, so the parent reads nothing until claude exits. The
// executor.go PTY path fixes this by attaching a pseudo-terminal so
// Node detects a TTY and flushes per line.
//
// This fake calls isatty(fd=1) at startup:
//   - TTY: flush after every write (line-buffered, matches Node TTY).
//   - pipe: wrap stdout in a large userspace buffer and never flush
//     until exit (matches Node pipe block-buffering).
//
// Tests pointing at this binary will pass when the executor attaches
// a pty (PTY path), and will FAIL when the executor attaches a pipe
// (the pre-PR-2 path that caused the stdout-silence bug).
//
// Env vars are identical to cmd/fake-claude:
//
//	FAKE_CLAUDE_DENY_TOOL=<name>
//	FAKE_CLAUDE_SESSION_ID=<id>
//	FAKE_CLAUDE_ARGS_FILE=<path>
//	FAKE_CLAUDE_STDERR=<text>
//	FAKE_CLAUDE_EXIT_CODE=<n>
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

func main() {
	if argsFile := os.Getenv("FAKE_CLAUDE_ARGS_FILE"); argsFile != "" {
		data, _ := json.Marshal(os.Args[1:])
		_ = os.WriteFile(argsFile, data, 0o600)
	}

	if msg := os.Getenv("FAKE_CLAUDE_STDERR"); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}
	if codeStr := os.Getenv("FAKE_CLAUDE_EXIT_CODE"); codeStr != "" {
		if code, err := strconv.Atoi(codeStr); err == nil {
			os.Exit(code)
		}
	}

	denyTool := os.Getenv("FAKE_CLAUDE_DENY_TOOL")
	sessionID := os.Getenv("FAKE_CLAUDE_SESSION_ID")
	if sessionID == "" {
		sessionID = "fake-session-blockbuf"
	}

	allowedSet := make(map[string]bool)
	for i, arg := range os.Args {
		if arg == "--allowedTools" && i+1 < len(os.Args) {
			for _, t := range strings.Split(os.Args[i+1], ",") {
				allowedSet[strings.TrimSpace(t)] = true
			}
		}
	}

	// Replicate Node/libuv's stdout mode choice.
	isTTY := term.IsTerminal(int(os.Stdout.Fd()))
	var out io.Writer
	var finalFlush func()
	if isTTY {
		// Line buffered: flush after every write. Matches Node on TTY.
		out = os.Stdout
		finalFlush = func() {}
	} else {
		// Block buffered: 64 KiB internal buffer, only flushed on
		// process exit (via the deferred call). Matches Node on pipe.
		bw := bufio.NewWriterSize(os.Stdout, 65536)
		out = bw
		finalFlush = func() { _ = bw.Flush() }
	}
	defer finalFlush()

	turnNum := 0
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	writeJSON := func(v any) {
		b, _ := json.Marshal(v)
		out.Write(b)
		out.Write([]byte{'\n'})
		// No explicit flush here: when isTTY, os.Stdout is unbuffered
		// so the write already hit the kernel; when !isTTY, we
		// deliberately let the bufio.Writer hold the bytes.
	}

	for scanner.Scan() {
		var msg map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		turnNum++

		writeJSON(map[string]any{
			"type": "stream_event",
			"event": map[string]any{
				"delta": map[string]any{"text": "fake delta"},
			},
		})

		writeJSON(map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "fake response"},
				},
			},
		})

		result := map[string]any{
			"type":           "result",
			"subtype":        "success",
			"total_cost_usd": 0.0001,
			"duration_ms":    42,
			"usage": map[string]int{
				"input_tokens":  10,
				"output_tokens": 5,
			},
			"session_id": sessionID,
		}
		if turnNum == 1 && denyTool != "" && !allowedSet[denyTool] {
			result["permission_denials"] = []map[string]any{
				{
					"tool_name":   denyTool,
					"tool_use_id": "toolu_fake_001",
					"tool_input": map[string]any{
						"file_path": "/tmp/fake.txt",
						"content":   "fake content",
					},
				},
			}
		}
		writeJSON(result)
	}
	fmt.Fprintln(os.Stderr, "fake-claude-blockbuf: stdin closed, exiting")
}
