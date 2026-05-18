package fake

import (
	"strings"

	"github.com/dio/luwes/shared"
)

// BenchFilterHandle is a zero-allocation variant of FakeFilterHandle for benchmarks.
// It does not record mutations and uses SilentHeaderMap for Set/Add so the
// []string{value} recording in FakeHeaderMap does not show up as benchmark noise.
// Use FakeFilterHandle in tests where you need to assert on side effects.
// Use BenchFilterHandle in benchmarks where you need 0 allocs/op.
type BenchFilterHandle struct {
	*FakeFilterHandle
	silentReqHeaders *SilentHeaderMap
}

// NewBenchFilterHandle builds a BenchFilterHandle with a SilentHeaderMap
// for request headers.
func NewBenchFilterHandle(opts ...FilterHandleOption) *BenchFilterHandle {
	fh := NewFilterHandle(opts...)
	bh := &BenchFilterHandle{
		FakeFilterHandle: fh,
		silentReqHeaders: &SilentHeaderMap{FakeHeaderMap: fh.reqHeaders},
	}
	return bh
}

func (h *BenchFilterHandle) RequestHeaders() shared.HeaderMap { return h.silentReqHeaders }

// SilentHeaderMap wraps FakeHeaderMap but overrides Set/Add to skip the
// mutation-recording appends. Reads delegate to the embedded FakeHeaderMap.
// This avoids the []string{value} heap allocation that recording incurs.
type SilentHeaderMap struct {
	*FakeHeaderMap
}

// Set updates in place without recording the mutation.
func (h *SilentHeaderMap) Set(key, value string) {
	lower := asciiToLower(key)
	existing := h.headers[lower]
	if cap(existing) > 0 {
		h.headers[lower] = existing[:1]
		h.headers[lower][0] = value
	} else {
		h.headers[lower] = []string{value}
	}
}

// Add updates in place without recording the mutation.
func (h *SilentHeaderMap) Add(key, value string) {
	h.headers[asciiToLower(key)] = append(h.headers[asciiToLower(key)], value)
}

// asciiToLower is a zero-allocation lowercase for pure-ASCII HTTP header keys.
// Falls back to strings.ToLower for non-ASCII (which allocates but is rare).
func asciiToLower(s string) string {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			return strings.ToLower(s)
		}
	}
	return s
}

// -- compile-time interface checks --
var _ shared.HeaderMap = (*SilentHeaderMap)(nil)
var _ shared.HttpFilterHandle = (*BenchFilterHandle)(nil)

// SilentBodyBuffer wraps FakeBodyBuffer but returns a pre-allocated
// UnsafeEnvoyBuffer slice instead of allocating one on each GetChunks call.
// Use in benchmarks to eliminate the GetChunks alloc noise from the fake.
type SilentBodyBuffer struct {
	*FakeBodyBuffer
	chunks []shared.UnsafeEnvoyBuffer // pre-allocated, length 1
}

// NewSilentBodyBuffer wraps body in a SilentBodyBuffer.
func NewSilentBodyBuffer(body []byte) *SilentBodyBuffer {
	b := &SilentBodyBuffer{
		FakeBodyBuffer: NewFakeBodyBuffer(body),
		chunks:         make([]shared.UnsafeEnvoyBuffer, 1),
	}
	if len(body) > 0 {
		b.chunks[0] = shared.UnsafeEnvoyBuffer{
			Ptr: &body[0],
			Len: uint64(len(body)),
		}
	}
	return b
}

// GetChunks returns the pre-allocated slice without allocation.
func (b *SilentBodyBuffer) GetChunks() []shared.UnsafeEnvoyBuffer {
	if len(b.Body) == 0 {
		return b.chunks[:0]
	}
	b.chunks[0] = shared.UnsafeEnvoyBuffer{
		Ptr: &b.Body[0],
		Len: uint64(len(b.Body)),
	}
	return b.chunks[:1]
}

var _ shared.BodyBuffer = (*SilentBodyBuffer)(nil)
