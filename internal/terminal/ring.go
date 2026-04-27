package terminal

type ScrollbackRing struct {
	buf []byte
	cap int
	seq int64
}

func NewScrollbackRing(capacity int) *ScrollbackRing {
	if capacity < 0 {
		capacity = 0
	}
	return &ScrollbackRing{cap: capacity}
}

func (r *ScrollbackRing) Write(data []byte) {
	r.WriteChunk(r.seq, data)
}

func (r *ScrollbackRing) WriteChunk(seq int64, data []byte) {
	r.seq = seq
	if r.cap == 0 || len(data) == 0 {
		return
	}
	if len(data) >= r.cap {
		r.buf = append(r.buf[:0], data[len(data)-r.cap:]...)
		return
	}
	r.buf = append(r.buf, data...)
	if overflow := len(r.buf) - r.cap; overflow > 0 {
		copy(r.buf, r.buf[overflow:])
		r.buf = r.buf[:r.cap]
	}
}

func (r *ScrollbackRing) Bytes() []byte {
	out := make([]byte, len(r.buf))
	copy(out, r.buf)
	return out
}

func (r *ScrollbackRing) Seq() int64 {
	return r.seq
}
