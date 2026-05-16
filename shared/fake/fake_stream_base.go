package fake

import (
	"strings"
	"unsafe"

	"github.com/dio/luwes/shared"
)

// FakeHeaderMap implements shared.HeaderMap backed by a plain Go map.
// Keys are normalised to lowercase on construction and lookup.
type FakeHeaderMap struct {
	headers map[string][]string
	// Mutations are recorded for assertion in tests.
	Sets    []SetCall
	Adds    []AddCall
	Removes []string
}

type SetCall struct{ Key, Value string }
type AddCall struct{ Key, Value string }

func NewFakeHeaderMap(headers map[string]string) *FakeHeaderMap {
	m := make(map[string][]string, len(headers))
	for k, v := range headers {
		m[strings.ToLower(k)] = []string{v}
	}
	return &FakeHeaderMap{headers: m}
}

func NewFakeHeaderMapMulti(headers map[string][]string) *FakeHeaderMap {
	m := make(map[string][]string, len(headers))
	for k, v := range headers {
		m[strings.ToLower(k)] = v
	}
	return &FakeHeaderMap{headers: m}
}

func (h *FakeHeaderMap) Get(key string) []shared.UnsafeEnvoyBuffer {
	values := h.headers[strings.ToLower(key)]
	if len(values) == 0 {
		return nil
	}
	result := make([]shared.UnsafeEnvoyBuffer, len(values))
	for i, v := range values {
		result[i] = shared.UnsafeEnvoyBuffer{Ptr: unsafe.StringData(v), Len: uint64(len(v))}
	}
	return result
}

func (h *FakeHeaderMap) GetOne(key string) shared.UnsafeEnvoyBuffer {
	values := h.headers[strings.ToLower(key)]
	if len(values) == 0 {
		return shared.UnsafeEnvoyBuffer{}
	}
	v := values[0]
	return shared.UnsafeEnvoyBuffer{Ptr: unsafe.StringData(v), Len: uint64(len(v))}
}

func (h *FakeHeaderMap) GetAll() [][2]shared.UnsafeEnvoyBuffer {
	result := make([][2]shared.UnsafeEnvoyBuffer, 0, len(h.headers))
	for k, vs := range h.headers {
		for _, v := range vs {
			result = append(result, [2]shared.UnsafeEnvoyBuffer{
				{Ptr: unsafe.StringData(k), Len: uint64(len(k))},
				{Ptr: unsafe.StringData(v), Len: uint64(len(v))},
			})
		}
	}
	return result
}

func (h *FakeHeaderMap) Set(key, value string) {
	h.headers[strings.ToLower(key)] = []string{value}
	h.Sets = append(h.Sets, SetCall{key, value})
}

func (h *FakeHeaderMap) Add(key, value string) {
	lower := strings.ToLower(key)
	h.headers[lower] = append(h.headers[lower], value)
	h.Adds = append(h.Adds, AddCall{key, value})
}

func (h *FakeHeaderMap) Remove(key string) {
	delete(h.headers, strings.ToLower(key))
	h.Removes = append(h.Removes, key)
}

// GetString is a test-helper that returns the first value for a key as a Go string.
func (h *FakeHeaderMap) GetString(key string) string {
	buf := h.GetOne(key)
	if buf.Ptr == nil {
		return ""
	}
	return buf.ToUnsafeString()
}

// FakeBodyBuffer implements shared.BodyBuffer backed by a []byte.
type FakeBodyBuffer struct {
	Body []byte
}

func NewFakeBodyBuffer(body []byte) *FakeBodyBuffer {
	return &FakeBodyBuffer{Body: body}
}

func (b *FakeBodyBuffer) GetChunks() []shared.UnsafeEnvoyBuffer {
	if len(b.Body) == 0 {
		return nil
	}
	return []shared.UnsafeEnvoyBuffer{
		{Ptr: unsafe.SliceData(b.Body), Len: uint64(len(b.Body))},
	}
}

func (b *FakeBodyBuffer) GetSize() uint64 { return uint64(len(b.Body)) }

func (b *FakeBodyBuffer) Drain(n uint64) {
	if n >= uint64(len(b.Body)) {
		b.Body = b.Body[:0]
		return
	}
	b.Body = b.Body[n:]
}

func (b *FakeBodyBuffer) Append(data []byte) {
	b.Body = append(b.Body, data...)
}
