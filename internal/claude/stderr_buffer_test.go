package claude

import (
	"strings"
	"testing"
)

func TestStderrBufferLineCap(t *testing.T) {
	b := newStderrBuffer(3, 1024)
	b.add("one")
	b.add("two")
	b.add("three")
	b.add("four")
	got := b.String()
	want := "two\nthree\nfour"
	if got != want {
		t.Errorf("line cap eviction: got %q, want %q", got, want)
	}
}

func TestStderrBufferByteCap(t *testing.T) {
	b := newStderrBuffer(100, 10)
	b.add("aaaa") // 4
	b.add("bbbb") // 4, total 8
	b.add("cccc") // 4, total 12 > 10 → evict "aaaa" → total 8
	got := b.String()
	want := "bbbb\ncccc"
	if got != want {
		t.Errorf("byte cap eviction: got %q, want %q", got, want)
	}
}

func TestStderrBufferLongLineTruncated(t *testing.T) {
	b := newStderrBuffer(10, 16)
	r := strings.NewReader(strings.Repeat("x", 100) + "\n")
	b.drain(r)
	got := b.String()
	if len(got) != 16 {
		t.Errorf("expected truncated line of length 16, got %d: %q", len(got), got)
	}
}

func TestStderrBufferDrainEOF(t *testing.T) {
	b := newStderrBuffer(10, 1024)
	r := strings.NewReader("alpha\nbeta\ngamma\n")
	b.drain(r) // must return once the reader hits EOF
	got := b.String()
	want := "alpha\nbeta\ngamma"
	if got != want {
		t.Errorf("drain: got %q, want %q", got, want)
	}
}

func TestStderrBufferEmpty(t *testing.T) {
	b := newStderrBuffer(10, 1024)
	if got := b.String(); got != "" {
		t.Errorf("empty buffer: got %q, want empty", got)
	}
}
