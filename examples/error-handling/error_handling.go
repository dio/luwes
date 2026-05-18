// Package errorhandling demonstrates how to observe and handle errors across the
// full request/response path in a luwes filter.
//
// # Scenarios covered
//
//   - Callout init failure: cluster not found or missing required headers.
//     The filter detects this at call time and sends 503 immediately.
//
//   - Callout network failure: upstream reset or buffer limit exceeded.
//     The callout callback receives result != HttpCalloutSuccess.
//     The filter sends 502 and emits a counter.
//
//   - Callout upstream error: callout succeeds but returns a 4xx/5xx.
//     The filter reads the :status header from the callout response and
//     decides whether to forward the request or reject it.
//
//   - Downstream local reply (OnLocalReply): Envoy itself decides to send a
//     local reply (e.g. upstream timeout, circuit breaker, buffer overflow).
//     The filter observes the code and details string, emits a counter, and
//     optionally adds a response header for client-side diagnostics.
//
//   - Response flags in OnStreamComplete: after every request, read
//     AttributeIDResponseFlags to detect upstream errors (UF, UH, UC, UT, ...)
//     and downstream resets (DC). Log and increment per-flag counters.
//
// None of these scenarios require the filter to buffer or modify the body.
// The filter can be layered in front of any upstream cluster.
package errorhandling

import (
	"strings"
	"sync"

	"github.com/dio/luwes/shared"
)

// Filter is a per-request instance, pooled by Factory.
type Filter struct {
	shared.EmptyHttpFilter

	handle  shared.HttpFilterHandle
	factory *Factory

	// path is copied from the request :path header in OnRequestHeaders.
	// Retained for structured logging in OnLocalReply and OnStreamComplete.
	path string
}

// Factory is created once per filter config.
type Factory struct {
	// Metrics: all defined at config load, never per-request.
	calloutInitFail    shared.MetricID // callout cluster not found / init error
	calloutNetFail     shared.MetricID // callout reset or buffer overflow
	calloutUpstreamErr shared.MetricID // callout upstream returned 4xx/5xx
	localReplyObserved shared.MetricID // OnLocalReply fired (Envoy-generated error)
	responseFlags      shared.MetricID // non-empty response flags in OnStreamComplete

	pool sync.Pool
}

// NewFactory is called once at Envoy config load per filter-chain.
func NewFactory(h shared.HttpFilterConfigHandle, _ []byte) (shared.HttpFilterFactory, error) {
	f := &Factory{}
	f.pool.New = func() any { return &Filter{factory: f} }

	if h == nil {
		return f, nil
	}

	var res shared.MetricsResult
	f.calloutInitFail, res = h.DefineCounter("error_handling_callout_init_fail", "reason")
	if res != shared.MetricsSuccess {
		f.calloutInitFail = 0
	}
	f.calloutNetFail, res = h.DefineCounter("error_handling_callout_net_fail", "result")
	if res != shared.MetricsSuccess {
		f.calloutNetFail = 0
	}
	f.calloutUpstreamErr, res = h.DefineCounter("error_handling_callout_upstream_err", "status")
	if res != shared.MetricsSuccess {
		f.calloutUpstreamErr = 0
	}
	f.localReplyObserved, res = h.DefineCounter("error_handling_local_reply", "code")
	if res != shared.MetricsSuccess {
		f.localReplyObserved = 0
	}
	f.responseFlags, res = h.DefineCounter("error_handling_response_flags", "flags")
	if res != shared.MetricsSuccess {
		f.responseFlags = 0
	}

	return f, nil
}

// Create returns a pooled Filter for the request.
func (f *Factory) Create(handle shared.HttpFilterHandle) shared.HttpFilter {
	filter := f.pool.Get().(*Filter)
	filter.handle = handle
	filter.path = ""
	return filter
}

func (f *Factory) OnDestroy() {}

// OnRequestHeaders copies the request path and initiates a callout to the
// "auth" cluster to validate the request.
//
// Error path 1: callout cluster not found or init failure.
// The filter sends 503 immediately and emits callout_init_fail.
func (f *Filter) OnRequestHeaders(headers shared.HeaderMap, _ bool) shared.HeadersStatus {
	// Retain path for downstream observability callbacks.
	var pathBuf shared.UnsafeEnvoyBuffer
	if headers.GetOneInto(":path", &pathBuf) {
		f.path = pathBuf.ToString()
	}

	init, _ := f.handle.HttpCallout(
		"auth",
		[][2]string{
			{":method", "GET"},
			{":path", f.path},
			{":scheme", "http"},
			{":authority", "auth"},
		},
		nil,
		500, // 500 ms timeout
		f,
	)

	if init != shared.HttpCalloutInitSuccess {
		reason := calloutInitReason(init)
		if f.factory.calloutInitFail != 0 {
			f.handle.IncrementCounterValue(f.factory.calloutInitFail, 1, reason)
		}
		if f.handle.LogEnabled(shared.LogLevelWarn) {
			f.handle.Log(shared.LogLevelWarn,
				"error-handling: callout init failed path=%s reason=%s", f.path, reason)
		}
		f.handle.SendLocalResponse(503, nil,
			[]byte(`{"error":"auth unavailable","reason":"`+reason+`"}`),
			"callout_init_fail",
		)
		return shared.HeadersStatusStop
	}

	// Callout in flight: Envoy pauses the request.
	return shared.HeadersStatusStop
}

// OnHttpCalloutDone is called on the Envoy worker thread when the callout
// completes. Handles two error paths:
//
// Error path 2: network failure (upstream reset, buffer overflow).
// Error path 3: callout succeeded but upstream returned a non-2xx status.
func (f *Filter) OnHttpCalloutDone(
	_ uint64,
	result shared.HttpCalloutResult,
	headers [][2]shared.UnsafeEnvoyBuffer,
	_ []shared.UnsafeEnvoyBuffer,
) {
	// Error path 2: network/transport failure.
	if result != shared.HttpCalloutSuccess {
		label := calloutResultLabel(result)
		if f.factory.calloutNetFail != 0 {
			f.handle.IncrementCounterValue(f.factory.calloutNetFail, 1, label)
		}
		f.handle.Log(shared.LogLevelError,
			"error-handling: callout network failure path=%s result=%s", f.path, label)
		f.handle.SendLocalResponse(502, nil,
			[]byte(`{"error":"auth unreachable"}`),
			"callout_net_fail",
		)
		return
	}

	// Extract :status from callout response headers.
	status := calloutStatus(headers)

	// Error path 3: upstream auth returned a non-2xx.
	if !strings.HasPrefix(status, "2") {
		if f.factory.calloutUpstreamErr != 0 {
			f.handle.IncrementCounterValue(f.factory.calloutUpstreamErr, 1, status)
		}
		if f.handle.LogEnabled(shared.LogLevelInfo) {
			f.handle.Log(shared.LogLevelInfo,
				"error-handling: callout upstream error path=%s status=%s", f.path, status)
		}
		code := uint32(403)
		if status == "401" {
			code = 401
		}
		f.handle.SendLocalResponse(code, nil,
			[]byte(`{"error":"auth denied","upstream_status":"`+status+`"}`),
			"callout_upstream_err",
		)
		return
	}

	// Auth passed: inject the upstream-provided identity header and continue.
	user := calloutHeader(headers, "x-auth-user")
	if user != "" {
		f.handle.RequestHeaders().Set("x-auth-user", user)
	}
	f.handle.ContinueRequest()
}

// OnLocalReply is called when Envoy itself decides to send a local reply:
// upstream timeout, circuit breaker open, rate limit, buffer overflow, etc.
// This is NOT called for replies generated by the filter's own SendLocalResponse.
//
// Error path 4: Envoy-generated local reply.
// We observe the code and details, emit a counter, and add a diagnostic header
// so the client can distinguish Envoy-level errors from upstream errors.
func (f *Filter) OnLocalReply(
	responseCode uint32,
	details shared.UnsafeEnvoyBuffer,
	resetImminent bool,
) shared.LocalReplyStatus {
	detailStr := details.ToString()
	codeStr := itoa(responseCode)

	if f.factory.localReplyObserved != 0 {
		f.handle.IncrementCounterValue(f.factory.localReplyObserved, 1, codeStr)
	}
	if f.handle.LogEnabled(shared.LogLevelWarn) {
		f.handle.Log(shared.LogLevelWarn,
			"error-handling: local reply path=%s code=%s details=%s reset=%v",
			f.path, codeStr, detailStr, resetImminent,
		)
	}

	// Add a response header so the client can see the Envoy-level error reason.
	// ResponseHeaders() is available here (before the local reply is sent).
	if !resetImminent {
		f.handle.ResponseHeaders().Set("x-error-details", detailStr)
	}

	return shared.LocalReplyStatusContinue
}

// OnStreamComplete fires after every request, whether successful or not.
// This is the place to observe the final outcome and emit per-flag counters.
//
// Error path 5: upstream or downstream errors visible as response flags.
// Common flags:
//   - UF: upstream connection failure
//   - UH: no healthy upstream
//   - UC: upstream connection termination
//   - UT: upstream request timeout
//   - DC: downstream connection termination (client disconnected)
func (f *Filter) OnStreamComplete() {
	flagsBuf, ok := f.handle.GetAttributeString(shared.AttributeIDResponseFlags)
	if ok && flagsBuf.Len > 0 {
		flags := flagsBuf.ToString()
		if f.factory.responseFlags != 0 {
			f.handle.IncrementCounterValue(f.factory.responseFlags, 1, flags)
		}
		if f.handle.LogEnabled(shared.LogLevelInfo) {
			f.handle.Log(shared.LogLevelInfo,
				"error-handling: stream complete path=%s flags=%s", f.path, flags)
		}
	}

	// Return filter instance to pool.
	f.handle = nil
	f.factory.pool.Put(f)
}

// calloutInitReason maps HttpCalloutInitResult to a short label for metrics.
func calloutInitReason(r shared.HttpCalloutInitResult) string {
	switch r {
	case shared.HttpCalloutInitClusterNotFound:
		return "cluster_not_found"
	case shared.HttpCalloutInitMissingRequiredHeaders:
		return "missing_headers"
	case shared.HttpCalloutInitCannotCreateRequest:
		return "cannot_create_request"
	case shared.HttpCalloutInitDuplicateCalloutId:
		return "duplicate_callout_id"
	default:
		return "unknown"
	}
}

// calloutResultLabel maps HttpCalloutResult to a short label for metrics.
func calloutResultLabel(r shared.HttpCalloutResult) string {
	switch r {
	case shared.HttpCalloutReset:
		return "reset"
	case shared.HttpCalloutExceedResponseBufferLimit:
		return "buffer_limit"
	default:
		return "unknown"
	}
}

// calloutStatus returns the :status value from callout response headers.
func calloutStatus(headers [][2]shared.UnsafeEnvoyBuffer) string {
	for _, h := range headers {
		if h[0].ToString() == ":status" {
			return h[1].ToString()
		}
	}
	return "unknown"
}

// calloutHeader returns the value of a named header from callout response headers.
func calloutHeader(headers [][2]shared.UnsafeEnvoyBuffer, name string) string {
	for _, h := range headers {
		if h[0].ToString() == name {
			return h[1].ToString()
		}
	}
	return ""
}

// itoa converts a uint32 to its decimal string without allocating.
func itoa(n uint32) string {
	if n == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte(n%10) + '0'
		n /= 10
	}
	return string(buf[i:])
}
