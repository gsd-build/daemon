package agentterminal

type ringChunk struct {
	seq  int64
	data []byte
}

type scrollbackRing struct {
	cap       int
	total     int
	seq       int64
	truncated bool
	chunks    []ringChunk
}

func newScrollbackRing(capacity int) *scrollbackRing {
	if capacity < 0 {
		capacity = 0
	}
	return &scrollbackRing{cap: capacity}
}

func (r *scrollbackRing) Write(data []byte) int64 {
	r.seq++
	r.WriteChunk(r.seq, data)
	return r.seq
}

func (r *scrollbackRing) WriteChunk(seq int64, data []byte) {
	r.seq = seq
	if r.cap == 0 || len(data) == 0 {
		return
	}
	chunk := append([]byte(nil), data...)
	if len(chunk) >= r.cap {
		chunk = chunk[len(chunk)-r.cap:]
		r.chunks = []ringChunk{{seq: seq, data: append([]byte(nil), chunk...)}}
		r.total = len(chunk)
		r.truncated = true
		return
	}
	r.chunks = append(r.chunks, ringChunk{seq: seq, data: chunk})
	r.total += len(chunk)
	r.trim()
}

func (r *scrollbackRing) TailBytes(limit int) ([]byte, bool) {
	if limit <= 0 || limit > r.cap {
		limit = r.cap
	}
	if limit <= 0 {
		return nil, r.total > 0
	}
	all := r.bytesFromChunks(r.chunks)
	truncated := r.truncated || len(all) > limit
	if truncated {
		all = all[len(all)-limit:]
	}
	return all, truncated
}

func (r *scrollbackRing) SinceSeq(seq int64, limit int) ([]byte, bool) {
	var selected []ringChunk
	for _, chunk := range r.chunks {
		if chunk.seq > seq {
			selected = append(selected, chunk)
		}
	}
	all := r.bytesFromChunks(selected)
	if limit <= 0 {
		limit = r.cap
	}
	if limit < 0 {
		limit = 0
	}
	if limit > len(all) {
		limit = len(all)
	}
	truncated := len(all) > limit
	if r.truncated && len(r.chunks) > 0 && seq < r.chunks[0].seq {
		truncated = true
	}
	if truncated && limit > 0 {
		all = all[len(all)-limit:]
	} else if limit == 0 {
		all = nil
	}
	return all, truncated
}

func (r *scrollbackRing) StringTail(limit int) string {
	out, _ := r.TailBytes(limit)
	return string(out)
}

func (r *scrollbackRing) Seq() int64 {
	return r.seq
}

func (r *scrollbackRing) trim() {
	for r.total > r.cap && len(r.chunks) > 0 {
		overflow := r.total - r.cap
		first := r.chunks[0]
		if overflow >= len(first.data) {
			r.total -= len(first.data)
			r.chunks = r.chunks[1:]
			r.truncated = true
			continue
		}
		r.chunks[0].data = append([]byte(nil), first.data[overflow:]...)
		r.total -= overflow
		r.truncated = true
	}
}

func (r *scrollbackRing) bytesFromChunks(chunks []ringChunk) []byte {
	n := 0
	for _, chunk := range chunks {
		n += len(chunk.data)
	}
	out := make([]byte, 0, n)
	for _, chunk := range chunks {
		out = append(out, chunk.data...)
	}
	return out
}
