// Package observability demonstrates metrics, tracing, and structured log
// enrichment from a luwes filter.
//
// It is the reference example for all three observability signals:
//
//   - Metrics: counter (requests_total) and histogram (handler_duration_ms),
//     both with a bounded tag key (method) defined at config time.
//
//   - Tracing: reads the active Envoy tracing span, adds a tag, spawns a child
//     span for the handler work, and finishes it on completion.
//
//   - Logs: enriches access logs via SetMetadata so downstream access loggers
//     can emit structured fields; also calls handle.Log for filter-level events.
//
// This is not a production filter. It exists to show each signal clearly with
// minimal noise. The comments explain every decision.
package observability

import (
	"time"

	"github.com/dio/luwes/shared"
)

// Factory holds metric IDs defined once at config load time.
// MetricIDs are config-scoped: they are valid for the lifetime of this factory
// and must not be used after OnDestroy.
type Factory struct {
	// requestsTotal counts requests, tagged by HTTP method.
	// Defined without a tag: avoids per-request symbol table writes.
	requestsTotal shared.MetricID

	// handlerDurationMs is a histogram of time spent in OnRequestHeaders.
	// Using ms as the unit keeps values readable in Envoy's /stats endpoint.
	handlerDurationMs shared.MetricID
}

// Filter holds per-request state.
type Filter struct {
	shared.EmptyHttpFilter
	handle shared.HttpFilterHandle
	f      *Factory

	// method is copied from request headers in OnRequestHeaders and used in
	// OnResponseHeaders for the histogram tag. ToString() ensures the value
	// survives past the callback boundary.
	method string

	// start records the time OnRequestHeaders was entered.
	start time.Time
}

// NewFactory satisfies sdk.FactoryFunc. Defines all metrics at config load time
// so per-request increments are free of symbol table writes.
func NewFactory(h shared.HttpFilterConfigHandle, _ []byte) (shared.HttpFilterFactory, error) {
	f := &Factory{}

	// Counter: no tag keys here, pure atomic increment per request.
	// To add a method tag: DefineCounter("requests_total", "method")
	// but then IncrementCounterValue needs a tag value on every request
	// (1 slice alloc + 1 symbol table write). Only use tags if you need the
	// dimension, and only when the tag value cardinality is bounded by config.
	id, res := h.DefineCounter("luwes_observability_requests_total")
	if res == shared.MetricsSuccess {
		f.requestsTotal = id
	}

	// Histogram: latency in milliseconds, no tags.
	// Envoy computes p50/p95/p99 from this automatically at /stats?format=json.
	id, res = h.DefineHistogram("luwes_observability_handler_duration_ms")
	if res == shared.MetricsSuccess {
		f.handlerDurationMs = id
	}

	return f, nil
}

func (f *Factory) Create(handle shared.HttpFilterHandle) shared.HttpFilter {
	return &Filter{handle: handle, f: f}
}

func (f *Factory) OnDestroy() {}

// OnRequestHeaders is the hot path.
//
// Metrics: increment request counter (atomic, zero alloc for no-tag counters).
// Tracing: get the active Envoy span, tag it, spawn a child for handler work.
// Logs:    enrich access log via SetMetadata + emit a filter-level log line.
func (f *Filter) OnRequestHeaders(headers shared.HeaderMap, _ bool) shared.HeadersStatus {
	// -- Timing (for histogram in OnResponseHeaders) --
	f.start = time.Now()

	// -- Method (bounded tag value, safe to use as histogram tag) --
	// GetOne is zero-alloc on the Go side (valueView escapes at CGO boundary,
	// structural cost present in both upstream SDK and luwes).
	// ToString() copies into Go memory, safe to use after callback returns.
	f.method = headers.GetOne(":method").ToString()

	// -- Metrics: increment request counter --
	// No variadic args = no slice alloc, no symbol table write. Just atomic add.
	f.handle.IncrementCounterValue(f.f.requestsTotal, 1)

	// -- Tracing: annotate the active Envoy span --
	// GetActiveSpan returns the span Envoy created for this request (if any
	// tracing provider is configured, e.g. Zipkin, Jaeger, OTLP). Returns nil
	// if no tracing is active; all dymSpan methods guard for nil span.
	span := f.handle.GetActiveSpan()
	if span != nil {
		// Tag the span with filter identity. Appears in the trace UI.
		span.SetTag("luwes.filter", "observability")
		span.SetTag("http.method", f.method)

		// Spawn a child span to measure the filter's own work.
		// The child is finished in OnResponseHeaders via the ChildSpan.Finish() call.
		// Child spans let you see filter latency separately from upstream latency
		// in traces, useful for identifying filter overhead.
		child := span.SpawnChild("luwes.observability.request")
		if child != nil {
			child.SetTag("phase", "request_headers")
			// Finish immediately since the filter does no async work.
			// For filters that suspend the chain (HeadersStatusStop + goroutine),
			// keep the child open and finish it when ContinueRequest is called.
			child.Finish()
		}
	}

	// -- Log enrichment: set metadata for access log formatters --
	// Envoy access loggers (file, gRPC, OTel) can emit dynamic filter metadata
	// via %DYNAMIC_METADATA(namespace:key)% in their format strings.
	// SetMetadata here means every access log entry for this request will carry
	// these fields without extra log calls.
	//
	// Example Envoy access log format:
	//   "[%DYNAMIC_METADATA(luwes:method)%] %DYNAMIC_METADATA(luwes:path)%"
	f.handle.SetMetadata("luwes", "method", f.method)
	path := headers.GetOne(":path").ToString()
	f.handle.SetMetadata("luwes", "path", path)

	// -- Filter-level log: only emitted when debug is enabled --
	// LogEnabled check avoids boxing method and path into interface{} when
	// the log level is above debug. Without this guard, the args are boxed
	// at the call site regardless of whether the message is emitted.
	if f.handle.LogEnabled(shared.LogLevelDebug) {
		f.handle.Log(shared.LogLevelDebug, "observability: %s %s", f.method, path)
	}

	return shared.HeadersStatusContinue
}

// OnResponseHeaders records the handler duration histogram.
// Called when upstream response headers arrive (or on direct_response).
func (f *Filter) OnResponseHeaders(headers shared.HeaderMap, _ bool) shared.HeadersStatus {
	// Record duration from request headers to response headers.
	// This measures total filter+upstream round-trip at the filter level.
	if !f.start.IsZero() {
		elapsedMs := uint64(time.Since(f.start).Milliseconds())
		if elapsedMs == 0 {
			elapsedMs = 1 // avoid recording 0 which can skew percentiles
		}
		// No tag args: no slice alloc, no symbol table write.
		f.handle.RecordHistogramValue(f.f.handlerDurationMs, elapsedMs)
	}

	// Enrich access log with response status.
	// Safe to call ToString on the status header; present in all responses.
	status := headers.GetOne(":status").ToString()
	f.handle.SetMetadata("luwes", "status", status)

	return shared.HeadersStatusContinue
}
