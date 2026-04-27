package terminal

import "testing"

func TestScrollbackRingKeepsMostRecentBytes(t *testing.T) {
	r := NewScrollbackRing(5)
	r.Write([]byte("abc"))
	r.Write([]byte("def"))

	if got := string(r.Bytes()); got != "bcdef" {
		t.Fatalf("scrollback = %q, want %q", got, "bcdef")
	}
}

func TestScrollbackRingTracksSequence(t *testing.T) {
	r := NewScrollbackRing(10)
	r.WriteChunk(7, []byte("hello"))

	if r.Seq() != 7 {
		t.Fatalf("seq = %d, want 7", r.Seq())
	}
	if got := string(r.Bytes()); got != "hello" {
		t.Fatalf("bytes = %q", got)
	}
}
