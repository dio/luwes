package sahl

import (
	"log/slog"
	"unsafe"

	"github.com/dio/luwes/shared"
)

// Log level constants. Re-exported from shared so callers do not import shared.
const (
	LogTrace    = shared.LogLevelTrace
	LogDebug    = shared.LogLevelDebug
	LogInfo     = shared.LogLevelInfo
	LogWarn     = shared.LogLevelWarn
	LogError    = shared.LogLevelError
	LogCritical = shared.LogLevelCritical
)

// Request holds the incoming request visible to a HandlerFunc.
// Method, Path, and Host are pre-copied into Go memory at callback entry.
// All other headers are accessed lazily via Header.Get or Header.Peek.
type Request struct {
	// Header provides access to request headers. Values are copied into Go
	// memory on first access (Get) or returned as unsafe strings (Peek).
	Header Header

	// Method is the HTTP method, pre-copied from the :method pseudo-header.
	Method string

	// Path is the request path, pre-copied from the :path pseudo-header.
	Path string

	// Host is the request authority, pre-copied from the :authority pseudo-header.
	Host string

	// FilterName is the Envoy filter name this request matched.
	FilterName string

	handle shared.HttpFilterHandle
}

// Log emits a message to Envoy's logger at the given level.
func (r *Request) Log(level shared.LogLevel, format string, args ...any) {
	r.handle.Log(level, format, args...)
}

// LogAttrs emits a structured log message to Envoy's logger.
// Fields are encoded as logfmt-style text appended to msg.
func (r *Request) LogAttrs(level shared.LogLevel, msg string, attrs ...slog.Attr) {
	if r.FilterName != "" {
		attrs = append([]slog.Attr{slog.String("filter", r.FilterName)}, attrs...)
	}
	r.handle.Log(level, "%s", formatLogAttrs(msg, attrs...))
}

// Body reads and returns the full request body as a Go-owned []byte.
// Calls utility.ReadWholeRequestBody internally. Call from OnRequestBody or
// when endOfStream is true. Do not call on header-only filters.
func (r *Request) Body() []byte {
	return readBody(r.handle)
}

// reset prepares a pooled Request for a new request.
func (r *Request) reset(hm shared.HeaderMap, handle shared.HttpFilterHandle, name string) {
	r.Header.reset(hm)
	r.FilterName = name
	r.handle = handle

	// Pre-copy the three headers that every filter needs.
	// GetOneInto: 0 allocs on CGO path for the read itself.
	// ToString(): 1 alloc per field (unavoidable copy into Go memory).
	var buf shared.UnsafeEnvoyBuffer
	if hm.GetOneInto(":method", &buf) {
		r.Method = buf.ToString()
	} else {
		r.Method = ""
	}
	if hm.GetOneInto(":path", &buf) {
		r.Path = buf.ToString()
	} else {
		r.Path = ""
	}
	if hm.GetOneInto(":authority", &buf) {
		r.Host = buf.ToString()
	} else {
		r.Host = ""
	}
}

// Header provides ergonomic access to request headers.
// It wraps the underlying luwes HeaderMap with a lazy Go-memory cache.
type Header struct {
	hm    shared.HeaderMap
	cache map[string]string // lowercase key -> first value, Go-owned
}

// Get returns the first value for key as a Go-owned string, safe to use
// after the handler returns. Costs 1 string copy on the first access for
// each unique key; 0 on repeat access (cached).
//
// Key lookup is case-insensitive (normalized to lowercase).
func (h *Header) Get(key string) string {
	lower := asciiToLower(key)
	if v, ok := h.cache[lower]; ok {
		return v
	}
	var buf shared.UnsafeEnvoyBuffer
	if !h.hm.GetOneInto(lower, &buf) || buf.Len == 0 {
		return ""
	}
	v := buf.ToString() // 1 alloc: copy into Go memory
	if h.cache == nil {
		h.cache = make(map[string]string, 8)
	}
	h.cache[lower] = v
	return v
}

// Peek returns the first value for key as an unsafe string valid only during
// the current callback. Zero allocations on the CGO path.
//
// Do NOT store the returned string past the handler call. Use Get if you need
// a value that outlives the callback. Returns false if the key is not present.
func (h *Header) Peek(key string) (string, bool) {
	lower := asciiToLower(key)
	// Check cache first: if Get was already called for this key, return the
	// cached Go string (still safe to use as a Peek since it's in Go memory).
	if v, ok := h.cache[lower]; ok {
		return v, true
	}
	var buf shared.UnsafeEnvoyBuffer
	if !h.hm.GetOneInto(lower, &buf) || buf.Len == 0 {
		return "", false
	}
	// Return unsafe string pointing into Envoy memory. Valid only during callback.
	return unsafe.String(buf.Ptr, buf.Len), true
}

// reset clears the cache and rewires the underlying HeaderMap.
// Called from Request.reset. The cache map is reused (cleared, not reallocated)
// so after a few requests the map alloc disappears from the hot path.
func (h *Header) reset(hm shared.HeaderMap) {
	h.hm = hm
	for k := range h.cache {
		delete(h.cache, k)
	}
}

// asciiToLower returns a lowercase version of s with no allocation when s is
// already lowercase ASCII. Falls back to the slower path for uppercase or
// non-ASCII input.
func asciiToLower(s string) string {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			// Allocates: only on first call for a mixed-case key.
			// Canonically-lowercase header names (x-api-key, :path, etc.) never hit this.
			b := make([]byte, len(s))
			copy(b, s)
			for j := i; j < len(b); j++ {
				if b[j] >= 'A' && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

// formatLogAttrs encodes msg + slog.Attr fields as logfmt-style text.
func formatLogAttrs(msg string, attrs ...slog.Attr) string {
	if len(attrs) == 0 {
		return msg
	}
	b := make([]byte, 0, len(msg)+len(attrs)*16)
	b = append(b, msg...)
	for _, a := range attrs {
		b = append(b, ' ')
		b = append(b, a.Key...)
		b = append(b, '=')
		b = append(b, a.Value.String()...)
	}
	return string(b)
}

// readBody reads the complete request body via luwes utility.
func readBody(handle shared.HttpFilterHandle) []byte {
	buf := handle.BufferedRequestBody()
	recv := handle.ReceivedRequestBody()
	isBuffered := handle.ReceivedBufferedRequestBody()
	size := buf.GetSize()
	if !isBuffered {
		size += recv.GetSize()
	}
	body := make([]byte, 0, size)
	for _, chunk := range buf.GetChunks() {
		body = append(body, chunk.ToUnsafeBytes()...)
	}
	if !isBuffered {
		for _, chunk := range recv.GetChunks() {
			body = append(body, chunk.ToUnsafeBytes()...)
		}
	}
	return body
}
