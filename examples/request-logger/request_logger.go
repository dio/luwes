// Package requestlogger records the full observable state of each request:
// headers, body (optional), response status, upstream identity, error signals,
// latency, and trace context. Emits one structured log record per request at
// stream completion and tags the active OTel span with all collected fields.
//
// # Config
//
//	{
//	  "record_request_headers":  true,   // capture all request headers
//	  "record_response_headers": true,   // capture all response headers
//	  "record_request_body":     false,  // buffer and capture request body
//	  "record_response_body":    false,  // buffer and capture response body
//	  "max_body_bytes":          4096    // truncate bodies at this size (default 4096)
//	}
//
// Body buffering adds latency and memory pressure. Leave disabled unless you
// need to record prompts/completions or debug payload-level failures. When
// enabled, the filter buffers the full body before forwarding it; upstream
// sees no difference in the final request but TTFB increases by the body
// transit time.
//
// # What it records
//
// All callbacks populate a per-request [record] that is written to Envoy's
// dynamic metadata namespace "req_log" and to the active OTel span at stream
// completion. Fields:
//
//	request:  id, method, path, host, headers, body (if configured)
//	response: status, headers (if configured), body (if configured)
//	upstream: address, transport_failure_reason
//	error:    local_reply_details (from OnLocalReply), response_flags,
//	          response_code_details
//	timing:   duration_ms, request_size_bytes, response_size_bytes
//	trace:    trace_id, span_id (from active span)
//
// # Correlation
//
// x-request-id is the primary correlation key. Envoy generates it automatically
// and propagates it upstream. The trace_id from the active span is the same ID
// used by the OTel access log exporter, so every log record can be joined to
// its trace in Jaeger/Tempo by either key.
//
// # SetMetadata
//
// All fields are written to the "req_log" dynamic metadata namespace.
// Access log format strings can reference them via:
//
//	%DYNAMIC_METADATA(req_log:field)%
//
// The OTel access log exporter promotes these to structured log record attributes,
// and since the log record carries the trace context (trace_id, span_id), every
// field is queryable in both the log backend (Loki, CloudLogging) and the trace
// backend (Jaeger, Tempo) without joining.
//
// See envoy.yaml for a complete wiring example.
package requestlogger

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/dio/luwes/shared"
)

const (
	metaNS         = "req_log"
	defaultMaxBody = 4096
)

// Config is parsed from the per-listener filter config JSON.
type Config struct {
	RecordRequestHeaders  bool  `json:"record_request_headers"`
	RecordResponseHeaders bool  `json:"record_response_headers"`
	RecordRequestBody     bool  `json:"record_request_body"`
	RecordResponseBody    bool  `json:"record_response_body"`
	MaxBodyBytes          int64 `json:"max_body_bytes"`
}

// record is the per-request state accumulated across callbacks.
// All fields are Go-owned copies safe past the callback boundary.
type record struct {
	// OnRequestHeaders
	requestID      string
	method         string
	path           string
	host           string
	traceID        string
	spanID         string
	requestHeaders [][2]string // nil if not configured

	// OnRequestBody
	requestBody []byte // nil if not configured or empty

	// OnResponseHeaders
	upstreamStatus  string
	upstreamAddress string
	responseHeaders [][2]string // nil if not configured

	// OnLocalReply
	errorDetails string

	// OnResponseBody
	responseBody []byte // nil if not configured or empty

	// OnStreamComplete (attributes)
	durationMs          float64
	requestSizeBytes    float64
	responseSizeBytes   float64
	responseCode        float64
	responseFlags       string
	responseCodeDetails string
	upstreamFailure     string
}

// Filter is a per-request instance, pooled by Factory.
type Filter struct {
	shared.EmptyHttpFilter
	handle  shared.HttpFilterHandle
	factory *Factory
	rec     record
}

// Factory is created once per filter-chain.
type Factory struct {
	cfg  Config
	pool sync.Pool
}

// NewFactory parses config and initialises the filter pool.
func NewFactory(_ shared.HttpFilterConfigHandle, raw []byte) (shared.HttpFilterFactory, error) {
	f := &Factory{
		cfg: Config{
			RecordRequestHeaders:  true,
			RecordResponseHeaders: true,
			MaxBodyBytes:          defaultMaxBody,
		},
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &f.cfg); err != nil {
			return nil, err
		}
	}
	if f.cfg.MaxBodyBytes <= 0 {
		f.cfg.MaxBodyBytes = defaultMaxBody
	}
	f.pool.New = func() any { return &Filter{factory: f} }
	return f, nil
}

func (f *Factory) Create(handle shared.HttpFilterHandle) shared.HttpFilter {
	filter := f.pool.Get().(*Filter)
	filter.handle = handle
	filter.rec = record{}
	return filter
}

func (f *Factory) OnDestroy() {}

// OnRequestHeaders collects request identity, trace context, and headers.
// If body recording is enabled it signals Envoy to buffer the request body.
func (f *Filter) OnRequestHeaders(headers shared.HeaderMap, _ bool) shared.HeadersStatus {
	r := &f.rec
	var (
		reqIDBuf  shared.UnsafeEnvoyBuffer
		methodBuf shared.UnsafeEnvoyBuffer
		pathBuf   shared.UnsafeEnvoyBuffer
		hostBuf   shared.UnsafeEnvoyBuffer
	)
	if headers.GetOneInto("x-request-id", &reqIDBuf) {
		r.requestID = reqIDBuf.ToString()
	}
	if headers.GetOneInto(":method", &methodBuf) {
		r.method = methodBuf.ToString()
	}
	if headers.GetOneInto(":path", &pathBuf) {
		r.path = pathBuf.ToString()
	}
	if headers.GetOneInto(":authority", &hostBuf) {
		r.host = hostBuf.ToString()
	}

	if span := f.handle.GetActiveSpan(); span != nil {
		if id, ok := span.GetTraceID(); ok {
			r.traceID = id.ToString()
		}
		if id, ok := span.GetSpanID(); ok {
			r.spanID = id.ToString()
		}
	}

	if f.factory.cfg.RecordRequestHeaders {
		r.requestHeaders = copyHeaders(headers.GetAll())
	}

	if f.factory.cfg.RecordRequestBody {
		return shared.HeadersStatusStopAllAndBuffer
	}
	return shared.HeadersStatusContinue
}

// OnRequestBody captures the request body when buffering is enabled.
func (f *Filter) OnRequestBody(body shared.BodyBuffer, endStream bool) shared.BodyStatus {
	if !endStream {
		return shared.BodyStatusStopAndBuffer
	}
	chunks := body.GetChunks()
	if len(chunks) > 0 {
		f.rec.requestBody = truncate(chunks[0].ToUnsafeBytes(), f.factory.cfg.MaxBodyBytes)
	}
	return shared.BodyStatusContinue
}

// OnResponseHeaders collects the upstream status, upstream address, and headers.
// If response body recording is enabled it signals Envoy to buffer the body.
func (f *Filter) OnResponseHeaders(headers shared.HeaderMap, _ bool) shared.HeadersStatus {
	r := &f.rec
	var statusBuf shared.UnsafeEnvoyBuffer
	if headers.GetOneInto(":status", &statusBuf) {
		r.upstreamStatus = statusBuf.ToString()
	}

	if addr, ok := f.handle.GetAttributeString(shared.AttributeIDUpstreamAddress); ok {
		r.upstreamAddress = addr.ToString()
	}

	if f.factory.cfg.RecordResponseHeaders {
		r.responseHeaders = copyHeaders(headers.GetAll())
	}

	if f.factory.cfg.RecordResponseBody {
		return shared.HeadersStatusContinue
	}
	return shared.HeadersStatusContinue
}

// OnResponseBody captures the response body when buffering is enabled.
func (f *Filter) OnResponseBody(body shared.BodyBuffer, endStream bool) shared.BodyStatus {
	if !f.factory.cfg.RecordResponseBody {
		return shared.BodyStatusContinue
	}
	if !endStream {
		return shared.BodyStatusStopAndBuffer
	}
	chunks := body.GetChunks()
	if len(chunks) > 0 {
		f.rec.responseBody = truncate(chunks[0].ToUnsafeBytes(), f.factory.cfg.MaxBodyBytes)
	}
	return shared.BodyStatusContinue
}

// OnLocalReply captures the Envoy-generated error details string.
// Fires on upstream timeout, circuit breaker, rate limit: NOT on the
// filter's own SendLocalResponse calls.
func (f *Filter) OnLocalReply(_ uint32, details shared.UnsafeEnvoyBuffer, _ bool) shared.LocalReplyStatus {
	f.rec.errorDetails = details.ToString()
	return shared.LocalReplyStatusContinue
}

// OnStreamComplete reads the final request attributes, tags the active span,
// writes all fields to dynamic metadata, and emits the log record.
//
// All numeric and string attributes (duration, flags, code_details, upstream
// failure reason) are only fully populated here, after the stream resolves.
func (f *Filter) OnStreamComplete() {
	r := &f.rec
	h := f.handle

	// Numeric attributes.
	if v, ok := h.GetAttributeNumber(shared.AttributeIDRequestDuration); ok {
		r.durationMs = v / 1e6 // nanoseconds -> milliseconds
	}
	if v, ok := h.GetAttributeNumber(shared.AttributeIDRequestSize); ok {
		r.requestSizeBytes = v
	}
	if v, ok := h.GetAttributeNumber(shared.AttributeIDResponseSize); ok {
		r.responseSizeBytes = v
	}
	if v, ok := h.GetAttributeNumber(shared.AttributeIDResponseCode); ok {
		r.responseCode = v
	}

	// String error attributes.
	if v, ok := h.GetAttributeString(shared.AttributeIDResponseFlags); ok && v.Len > 0 {
		r.responseFlags = v.ToString()
	}
	if v, ok := h.GetAttributeString(shared.AttributeIDResponseCodeDetails); ok && v.Len > 0 {
		r.responseCodeDetails = v.ToString()
	}
	if v, ok := h.GetAttributeString(shared.AttributeIDUpstreamTransportFailureReason); ok && v.Len > 0 {
		r.upstreamFailure = v.ToString()
	}

	// Tag the active span. All fields become span attributes in the OTel backend.
	if span := h.GetActiveSpan(); span != nil {
		span.SetTag("request.id", r.requestID)
		span.SetTag("request.method", r.method)
		span.SetTag("request.path", r.path)
		span.SetTag("request.host", r.host)
		span.SetTag("response.status", r.upstreamStatus)
		span.SetTag("upstream.address", r.upstreamAddress)
		span.SetTag("response.flags", r.responseFlags)
		span.SetTag("response.code_details", r.responseCodeDetails)
		if r.errorDetails != "" {
			span.SetTag("error", "true")
			span.SetTag("error.details", r.errorDetails)
		}
		if r.upstreamFailure != "" {
			span.SetTag("upstream.transport_failure", r.upstreamFailure)
		}
	}

	// Write all fields to dynamic metadata for access log formatters.
	setMeta := func(key, value string) {
		if value != "" {
			h.SetMetadata(metaNS, key, value)
		}
	}
	setMeta("request_id", r.requestID)
	setMeta("method", r.method)
	setMeta("path", r.path)
	setMeta("host", r.host)
	setMeta("trace_id", r.traceID)
	setMeta("span_id", r.spanID)
	setMeta("upstream_status", r.upstreamStatus)
	setMeta("upstream_address", r.upstreamAddress)
	setMeta("response_flags", r.responseFlags)
	setMeta("error_details", r.errorDetails)
	setMeta("response_code_details", r.responseCodeDetails)
	setMeta("upstream_failure", r.upstreamFailure)
	if len(r.requestBody) > 0 {
		h.SetMetadata(metaNS, "request_body", string(r.requestBody))
	}
	if len(r.responseBody) > 0 {
		h.SetMetadata(metaNS, "response_body", string(r.responseBody))
	}

	// Emit structured log record (appears in Envoy error log).
	if h.LogEnabled(shared.LogLevelInfo) {
		h.Log(shared.LogLevelInfo,
			"req id=%s method=%s path=%s host=%s status=%.0f upstream=%s "+
				"flags=%s duration=%.2fms req_bytes=%.0f resp_bytes=%.0f "+
				"error_details=%s code_details=%s upstream_failure=%s "+
				"trace=%s span=%s",
			r.requestID, r.method, r.path, r.host, r.responseCode,
			r.upstreamAddress, r.responseFlags, r.durationMs,
			r.requestSizeBytes, r.responseSizeBytes,
			r.errorDetails, r.responseCodeDetails, r.upstreamFailure,
			r.traceID, r.spanID,
		)
	}

	f.handle = nil
	f.factory.pool.Put(f)
}

// copyHeaders copies header names and values into Go memory.
// GetAll returns UnsafeEnvoyBuffers valid only during the callback;
// we must copy before returning.
func copyHeaders(raw [][2]shared.UnsafeEnvoyBuffer) [][2]string {
	out := make([][2]string, len(raw))
	for i, h := range raw {
		out[i] = [2]string{h[0].ToString(), h[1].ToString()}
	}
	return out
}

// truncate copies at most maxBytes bytes from src into a new slice.
func truncate(src []byte, maxBytes int64) []byte {
	if int64(len(src)) <= maxBytes {
		dst := make([]byte, len(src))
		copy(dst, src)
		return dst
	}
	dst := make([]byte, maxBytes)
	copy(dst, src[:maxBytes])
	return dst
}

// hasError returns true if the record contains any error signal.
func (r *record) hasError() bool {
	return r.errorDetails != "" ||
		r.upstreamFailure != "" ||
		(r.responseFlags != "" && containsErrorFlag(r.responseFlags))
}

func containsErrorFlag(flags string) bool {
	for _, f := range []string{"UF", "UH", "UC", "UT", "UO", "NR"} {
		if strings.Contains(flags, f) {
			return true
		}
	}
	return false
}
