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
	lower := strings.ToLower(key)
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
	lower := strings.ToLower(key)
	h.headers[lower] = append(h.headers[lower], value)
}

// -- compile-time interface checks --
var _ shared.HeaderMap = (*SilentHeaderMap)(nil)
var _ shared.HttpFilterHandle = (*BenchFilterHandle)(nil)
