// Package buffer provides zero-allocation stream buffering primitives for
// Envoy dynamic module filters.
//
// Designed for the pattern where you need to extract metadata from a streaming
// response body (SSE, NDJSON, chunked JSON) without buffering the entire stream.
// The head+tail pattern covers the common case where relevant data appears at
// the beginning and end of the stream (e.g. LLM token usage).
package buffer

import "slices"

// Ring is a fixed-size circular buffer that captures the LAST n bytes written.
// When full, new writes overwrite the oldest data.
// Zero-allocation after construction. Not goroutine-safe.
type Ring struct {
	data []byte
	size int
	pos  int
	full bool
}

// NewRing creates a Ring with the given capacity in bytes.
func NewRing(size int) *Ring {
	return &Ring{data: make([]byte, size), size: size}
}

// Write appends p to the ring, overwriting the oldest data when full.
func (rb *Ring) Write(p []byte) {
	for len(p) > 0 {
		n := copy(rb.data[rb.pos:], p)
		rb.pos += n
		p = p[n:]
		if rb.pos >= rb.size {
			rb.pos = 0
			rb.full = true
		}
	}
}

// Bytes returns the buffered content in chronological order (oldest first).
// Linearises the ring in-place via three-reversal rotation. O(n), no alloc.
// After Bytes() the ring is in a clean state for continued writes.
func (rb *Ring) Bytes() []byte {
	if !rb.full {
		return rb.data[:rb.pos]
	}
	slices.Reverse(rb.data[:rb.pos])
	slices.Reverse(rb.data[rb.pos:rb.size])
	slices.Reverse(rb.data[:rb.size])
	rb.pos = 0
	rb.full = false
	return rb.data[:rb.size]
}

// Reset clears the ring without releasing memory.
func (rb *Ring) Reset() {
	rb.pos = 0
	rb.full = false
}

// Len returns the number of bytes currently stored.
func (rb *Ring) Len() int {
	if rb.full {
		return rb.size
	}
	return rb.pos
}

// HeadTail captures both the first headSize bytes and the last tailSize bytes
// of a stream. This covers the LLM SSE pattern:
//   - Input token counts appear near the START (e.g. Anthropic message_start)
//   - Output token counts appear near the END (message_delta, final usage chunk)
//
// Usage:
//
//	ht := buffer.NewHeadTail(8*1024, 64*1024)
//	// In response handler, per chunk:
//	ht.Write(chunk.Data)
//	// On EndStream:
//	usage := ExtractUsage(ht.Head(), ht.Tail())
type HeadTail struct {
	head     []byte
	headN    int
	headSize int
	tail     *Ring
}

// NewHeadTail creates a HeadTail buffer with separate head and tail capacities.
func NewHeadTail(headSize, tailSize int) *HeadTail {
	return &HeadTail{
		head:     make([]byte, headSize),
		headSize: headSize,
		tail:     NewRing(tailSize),
	}
}

// Write feeds bytes into both the head slab and tail ring.
func (ht *HeadTail) Write(p []byte) {
	if ht.headN < ht.headSize {
		n := copy(ht.head[ht.headN:], p)
		ht.headN += n
	}
	ht.tail.Write(p)
}

// Head returns the first headSize bytes received (or fewer if stream was shorter).
func (ht *HeadTail) Head() []byte {
	return ht.head[:ht.headN]
}

// Tail returns the last tailSize bytes received, linearised in chronological order.
func (ht *HeadTail) Tail() []byte {
	return ht.tail.Bytes()
}

// Reset clears both head and tail without releasing memory.
func (ht *HeadTail) Reset() {
	ht.headN = 0
	ht.tail.Reset()
}
