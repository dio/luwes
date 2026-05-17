package sahl

import (
	"github.com/dio/luwes/shared"
)

// ResponseChunk is passed to a ResponseHandlerFunc on each response body chunk.
// It is valid only during the ResponseHandlerFunc call; do not retain Data past the
// handler return.
//
// Lifecycle:
//   - ResponseHandlerFunc is first called in OnResponseHeaders with Data=nil,
//     EndStream=false. Use this call to inspect headers and set up per-response
//     state via Context.
//   - Subsequently called in each OnResponseBody with Data set to the current
//     chunk. The chunk is Envoy-owned memory; copy if you need it past the call.
//   - EndStream is true on the final OnResponseBody call. No further calls occur.
type ResponseChunk struct {
	// StatusCode is the HTTP response status (e.g. 200).
	StatusCode int

	// ContentType is the value of the Content-Type response header,
	// pre-extracted for convenience. Empty string if absent.
	ContentType string

	// Data is the current body chunk. Nil during the OnResponseHeaders call.
	// Points into Envoy-owned memory; valid only during this call.
	Data []byte

	// EndStream is true when this is the last chunk (endOfStream from Envoy).
	// When true, no further ResponseHandlerFunc calls occur for this request.
	EndStream bool

	// Context is a per-request slot for the response handler to store state
	// across calls (headers call, each body chunk, final EndStream call).
	// sahl sets it to nil on filter pool return. The response handler owns
	// the lifecycle of whatever it stores here.
	Context *any
}

// ResponseHandlerFunc is called on each response body chunk and once (with
// Data=nil) on response headers. EndStream is true on the final call.
//
// Runs on the Envoy worker thread. Must not block. For observe-only use cases
// (tap SSE, record metrics, read token counts) return immediately after
// accumulating data into a local ring. The response is always forwarded to the
// downstream client regardless of what the handler does (BodyStatusContinue).
//
// To set response metadata or increment a counter after reading the body,
// call w.SetMetadata / w.IncrementCounter on the final call (EndStream=true).
type ResponseHandlerFunc func(w *Writer, chunk *ResponseChunk)

// responseState holds per-request state for response observation.
// Embedded in sahlFilter. Reset on pool return.
type responseState struct {
	chunk ResponseChunk
	ctx   any // backing store for chunk.Context; zeroed on reset
}

func (s *responseState) reset() {
	s.ctx = nil
	s.chunk = ResponseChunk{}
}

// onResponseHeaders is called by sahlFilter.OnResponseHeaders when a
// responseFn is registered. Snapshots status + Content-Type into state,
// then calls the handler with Data=nil to allow header-time setup.
func (f *sahlFilter) onResponseHeaders(headers shared.HeaderMap) {
	var statusBuf shared.UnsafeEnvoyBuffer
	if headers.GetOneInto(":status", &statusBuf) && statusBuf.Len > 0 {
		f.respState.chunk.StatusCode = parseStatus(statusBuf.ToUnsafeString())
	}

	var ctBuf shared.UnsafeEnvoyBuffer
	if headers.GetOneInto("content-type", &ctBuf) && ctBuf.Len > 0 {
		// Content-Type is pre-copied: handler may inspect it after this call returns.
		f.respState.chunk.ContentType = ctBuf.ToString()
	}

	f.respState.chunk.Data = nil
	f.respState.chunk.EndStream = false
	f.respState.chunk.Context = &f.respState.ctx
	f.handler.responseFn(f.writer, &f.respState.chunk)
}

// onResponseBody is called by sahlFilter.OnResponseBody on each chunk.
func (f *sahlFilter) onResponseBody(body shared.BodyBuffer, endStream bool) {
	chunks := body.GetChunks()
	var data []byte
	if len(chunks) > 0 {
		// ToUnsafeBytes: zero-copy view into Envoy memory. Valid only during this call.
		data = chunks[0].ToUnsafeBytes()
	} else {
		// Empty chunk (e.g. the trailing 0-length chunk that signals end of stream).
		// Use an empty non-nil slice so response handlers can distinguish this from
		// the headers call (Data==nil) using EndStream as the discriminator.
		data = []byte{}
	}
	f.respState.chunk.Data = data
	f.respState.chunk.EndStream = endStream
	f.respState.chunk.Context = &f.respState.ctx
	f.handler.responseFn(f.writer, &f.respState.chunk)
}

// parseStatus converts an ASCII status string like "200" to int.
// Avoids strconv.Atoi allocation on the hot path.
func parseStatus(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
