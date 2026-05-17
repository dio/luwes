// Package buffer provides zero-allocation stream buffering primitives for
// Envoy dynamic module filters.
//
// Designed for filters that need to extract metadata from a streaming response
// body (SSE, NDJSON, chunked JSON) without buffering the entire stream. The
// head+tail pattern covers the common case where relevant data appears at the
// beginning and end of the stream (e.g. LLM token usage counts).
//
// Integration with luwes:
//
//	func (f *Filter) OnResponseBody(body shared.BodyBuffer, endStream bool) shared.BodyStatus {
//	    f.ring.WriteChunks(body.GetChunks())   // zero-copy from Envoy memory
//	    if !endStream {
//	        return shared.BodyStatusContinue
//	    }
//	    usage := ExtractUsage(f.ring.Head(), f.ring.Tail())
//	    // ... emit metrics ...
//	    return shared.BodyStatusContinue
//	}
package buffer

import (
	"slices"

	"github.com/dio/luwes/shared"
)

// Ring is a fixed-size circular buffer that captures the LAST n bytes written.
// When full, new writes overwrite the oldest data.
// Zero-allocation after construction. Not goroutine-safe: intended for use
// within a single filter's OnResponseBody callback sequence (always on the
// same Envoy worker thread).
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

// WriteChunks appends all chunks from an Envoy body buffer to the ring.
// Each UnsafeEnvoyBuffer points into Envoy-owned memory; WriteChunks copies
// the bytes into the ring so they survive past the current callback.
// This is the idiomatic luwes entry point: call it with the slice returned
// by shared.BodyBuffer.GetChunks().
func (rb *Ring) WriteChunks(chunks []shared.UnsafeEnvoyBuffer) {
	for _, c := range chunks {
		rb.Write(c.ToUnsafeBytes())
	}
}

// Bytes returns the buffered content in chronological order (oldest first).
// Linearises the ring in-place via three-reversal rotation: O(n) time, O(1)
// space. After Bytes() the ring is in a clean linearised state; subsequent
// Write calls continue normally.
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
// of a stream without retaining the middle. This covers the LLM SSE pattern:
//
//   - Input token counts appear near the START (e.g. Anthropic message_start)
//   - Output token counts appear near the END (message_delta, final usage chunk)
//
// The head is a flat slab: writes stop once full. The tail is a Ring: new data
// overwrites the oldest, so it always holds the final tailSize bytes.
//
// Typical usage in a luwes response observer:
//
//	ht := buffer.NewHeadTail(8*1024, 64*1024)
//
//	// In ResponseHandlerFunc, per chunk:
//	ht.WriteChunks(body.GetChunks())   // or ht.Write(chunk.Data) in sahl
//
//	// On EndStream:
//	input, output := extractTokens(ht.Head(), ht.Tail())
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
// Once the head slab is full, further bytes are written to the tail only.
func (ht *HeadTail) Write(p []byte) {
	if ht.headN < ht.headSize {
		n := copy(ht.head[ht.headN:], p)
		ht.headN += n
	}
	ht.tail.Write(p)
}

// WriteChunks feeds all chunks from an Envoy body buffer into the HeadTail.
// Each UnsafeEnvoyBuffer points into Envoy-owned memory; WriteChunks copies
// the bytes so they survive past the current callback.
// This is the idiomatic luwes entry point: call it with the slice returned
// by shared.BodyBuffer.GetChunks() or shared.HttpFilterHandle.ReceivedResponseBody().GetChunks().
func (ht *HeadTail) WriteChunks(chunks []shared.UnsafeEnvoyBuffer) {
	for _, c := range chunks {
		ht.Write(c.ToUnsafeBytes())
	}
}

// Head returns the first headSize bytes received, or fewer if the stream
// was shorter than headSize.
func (ht *HeadTail) Head() []byte {
	return ht.head[:ht.headN]
}

// Tail returns the last tailSize bytes received, linearised in chronological
// order. Calls Ring.Bytes() which linearises in-place.
func (ht *HeadTail) Tail() []byte {
	return ht.tail.Bytes()
}

// Reset clears both head and tail without releasing memory. Safe to call
// between requests when reusing a HeadTail from a pool.
func (ht *HeadTail) Reset() {
	ht.headN = 0
	ht.tail.Reset()
}
