package agentterminal

import (
	"bytes"
	"testing"
)

func TestScrollbackRingWritesIncrementSequence(t *testing.T) {
	r := newScrollbackRing(64)
	if got := r.Write([]byte("a")); got != 1 {
		t.Fatalf("seq = %d, want 1", got)
	}
	if got := r.Write([]byte("b")); got != 2 {
		t.Fatalf("seq = %d, want 2", got)
	}
	if r.Seq() != 2 {
		t.Fatalf("Seq() = %d, want 2", r.Seq())
	}
}

func TestScrollbackRingTailBytesTruncatesOldest(t *testing.T) {
	r := newScrollbackRing(5)
	r.Write([]byte("abc"))
	r.Write([]byte("def"))
	got, truncated := r.TailBytes(5)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if string(got) != "bcdef" {
		t.Fatalf("tail = %q, want bcdef", got)
	}
}

func TestScrollbackRingSinceSeq(t *testing.T) {
	r := newScrollbackRing(64)
	r.Write([]byte("one"))
	r.Write([]byte("two"))
	r.Write([]byte("three"))
	got, _ := r.SinceSeq(1, 64)
	if string(got) != "twothree" {
		t.Fatalf("since seq = %q", got)
	}
}

func TestScrollbackRingReturnsCopies(t *testing.T) {
	r := newScrollbackRing(64)
	r.Write([]byte("abc"))
	got, _ := r.TailBytes(64)
	got[0] = 'z'
	again, _ := r.TailBytes(64)
	if bytes.Equal(got, again) {
		t.Fatal("tail shares backing storage")
	}
}
