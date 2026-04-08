package claude

import (
	"bufio"
	"io"
	"strings"
	"sync"
)

// stderrBuffer is a bounded line-oriented ring buffer used to capture the
// tail of a claude subprocess's stderr stream. It enforces two caps: a
// maximum number of lines and a maximum total byte count across retained
// lines. When either cap is exceeded, the oldest lines are evicted FIFO
// until both caps are satisfied again.
//
// A single instance is safe for concurrent use: drain writes to it from a
// goroutine, String may be read from any goroutine once drain has returned.
type stderrBuffer struct {
	mu       sync.Mutex
	maxLines int
	maxBytes int
	lines    []string
	bytes    int
}

func newStderrBuffer(maxLines, maxBytes int) *stderrBuffer {
	return &stderrBuffer{
		maxLines: maxLines,
		maxBytes: maxBytes,
	}
}

// drain reads lines from r until EOF (or any read error) and appends each
// one to the ring. Individual lines longer than maxBytes are truncated to
// maxBytes before being added. drain returns when the underlying reader
// signals EOF, which happens naturally when the subprocess closes its
// stderr — no separate cancellation channel is needed.
func (b *stderrBuffer) drain(r io.Reader) {
	scanner := bufio.NewScanner(r)
	// Allow individual stderr lines up to 64 KiB (hard limit before a
	// line is dropped entirely). Anything larger than b.maxBytes will be
	// truncated to fit inside the ring.
	scanner.Buffer(make([]byte, 4096), 64*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) > b.maxBytes {
			line = line[:b.maxBytes]
		}
		b.add(line)
	}
}

// add appends one line and evicts oldest lines while either cap is exceeded.
func (b *stderrBuffer) add(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.lines = append(b.lines, line)
	b.bytes += len(line)

	for len(b.lines) > b.maxLines || b.bytes > b.maxBytes {
		if len(b.lines) == 0 {
			break
		}
		b.bytes -= len(b.lines[0])
		b.lines = b.lines[1:]
	}
}

// String returns the current ring contents joined with newlines. Safe to
// call concurrently with add, but callers should normally wait for drain
// to return before reading for the final tail.
func (b *stderrBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.lines) == 0 {
		return ""
	}
	return strings.Join(b.lines, "\n")
}
