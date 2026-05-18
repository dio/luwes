// accesslogger.go: access logger half of the request-ui filter.
//
// The response handler deposits a partial record into Sink.PendingRecords when
// it sees the response headers (chunk.StatusCode != 0). This access logger pops
// it, enriches it with finalized stream fields, and calls Sink.Send so the
// record appears in the UI with correct duration, byte counts, and flags.
//
// Register from cmd/main.go alongside the HTTP filter.
package requestui

import (
	"fmt"
	"strings"
	"sync"

	"github.com/dio/luwes/sahl/examples/request-ui/sink"
	"github.com/dio/luwes/shared"
)

// PendingRecords maps request ID to a partially filled Record waiting for
// finalized fields from the access logger. Exported so cmd/main.go can pass
// the same map to both the filter (via Register) and the access logger factory.
type PendingRecords struct {
	m sync.Map
}

// Store deposits a record for the access logger to consume.
func (p *PendingRecords) Store(requestID string, r *sink.Record) {
	if requestID != "" {
		p.m.Store(requestID, r)
	}
}

// LoadAndDelete retrieves and removes a record by request ID.
func (p *PendingRecords) LoadAndDelete(requestID string) (*sink.Record, bool) {
	val, ok := p.m.LoadAndDelete(requestID)
	if !ok {
		return nil, false
	}
	r, ok := val.(*sink.Record)
	return r, ok
}

// Delete removes a record, used by cleanup paths.
func (p *PendingRecords) Delete(requestID string) {
	p.m.Delete(requestID)
}

// NewAccessLoggerFactory returns an access logger factory that pops partial
// records from pending, enriches them with finalized stream fields, and sends
// them to the sink.
func NewAccessLoggerFactory(
	pending *PendingRecords,
	s *sink.Sink,
) func(shared.AccessLoggerConfigHandle, []byte) (shared.AccessLoggerFactory, error) {
	return func(_ shared.AccessLoggerConfigHandle, _ []byte) (shared.AccessLoggerFactory, error) {
		return &alFactory{pending: pending, sink: s}, nil
	}
}

type alFactory struct {
	pending *PendingRecords
	sink    *sink.Sink
}

func (f *alFactory) NewLogger() shared.AccessLogger {
	return &alLogger{pending: f.pending, sink: f.sink}
}
func (f *alFactory) OnDestroy() {}

type alLogger struct {
	shared.EmptyAccessLogger
	pending *PendingRecords
	sink    *sink.Sink
}

func (l *alLogger) OnLog(h shared.AccessLoggerHandle, logType shared.AccessLogType) {
	if logType != shared.AccessLogTypeDownstreamEnd {
		return
	}

	ridBuf, ok := h.GetHeader(shared.HttpHeaderTypeRequest, "x-request-id")
	if !ok {
		return
	}
	key := ridBuf.ToString()
	if key == "" {
		return
	}

	r, ok := l.pending.LoadAndDelete(key)
	if !ok {
		// DC case: client disconnected before response headers arrived.
		// The response handler never fired so no record was deposited.
		// Build a minimal record from attributes available in the access logger.
		r = l.buildMinimalRecord(h, key)
		if r == nil {
			return
		}
	}

	// Enrich with finalized stream fields.
	timing := h.GetTimingInfo()
	if timing.RequestCompleteDurationNs >= 0 {
		r.DurationMs = float64(timing.RequestCompleteDurationNs) / 1e6
	}
	b := h.GetBytesInfo()
	r.RequestSizeBytes = float64(b.BytesReceived)
	r.ResponseSizeBytes = float64(b.BytesSent)

	if v, ok := h.GetAttributeInt(shared.AttributeIDResponseCode); ok && v > 0 {
		r.ResponseCode = float64(v)
	}
	if v, ok := h.GetAttributeString(shared.AttributeIDResponseCodeDetails); ok && v.Len > 0 {
		r.ResponseCodeDetails = v.ToString()
	}
	if flags := responseFlags(h.GetResponseFlags()); flags != "" {
		r.ResponseFlags = flags
	}
	if v, ok := h.GetAttributeString(shared.AttributeIDUpstreamTransportFailureReason); ok && v.Len > 0 {
		r.UpstreamFailure = v.ToString()
	}

	// Wire bytes (TLS overhead etc.)
	r.WireBytesReceived = b.WireBytesReceived
	r.WireBytesSent = b.WireBytesSent

	// Upstream connection pool wait time.
	if ns := h.GetUpstreamPoolReadyDurationNs(); ns >= 0 {
		r.UpstreamCxPoolReadyMs = float64(ns) / 1e6
	}

	// Upstream retry count (>1 means retries occurred).
	r.UpstreamRequestAttempts = h.GetUpstreamRequestAttemptCount()

	// Upstream local address (our side of the upstream connection).
	if v, ok := h.GetAttributeString(shared.AttributeIDUpstreamLocalAddress); ok && v.Len > 0 {
		r.UpstreamLocalAddress = v.ToString()
	}

	// Request protocol (HTTP/1.1, HTTP/2, HTTP/3).
	if v, ok := h.GetAttributeString(shared.AttributeIDRequestProtocol); ok && v.Len > 0 {
		r.RequestProtocol = v.ToString()
	}

	// Tracing from the access logger (more reliable than HTTP filter span).
	if v, ok := h.GetTraceID(); ok && v.Len > 0 {
		r.TraceIDFinal = v.ToString()
	}
	if v, ok := h.GetSpanID(); ok && v.Len > 0 {
		r.SpanIDFinal = v.ToString()
	}
	r.TraceSampled = h.IsTraceSampled()

	// Local reply body (for Envoy-generated 504/503/etc. responses).
	if v, ok := h.GetLocalReplyBody(); ok && v.Len > 0 {
		r.LocalReplyBody = v.ToString()
	}

	r.HasError = r.ErrorDetails != "" ||
		r.UpstreamFailure != "" ||
		(r.ResponseFlags != "" && containsErrorFlag(r.ResponseFlags)) ||
		r.ResponseCode >= 500

	l.sink.Send(r)
}

// responseFlagNames maps CoreResponseFlag bit positions to their short string
// representations, matching Envoy's %RESPONSE_FLAGS% access log format.
// Indexed by bit position; must stay in sync with CoreResponseFlag in abi.h.
var responseFlagNames = [...]string{
	"LH",    // 0  FailedLocalHealthCheck
	"UH",    // 1  NoHealthyUpstream
	"UT",    // 2  UpstreamRequestTimeout
	"LR",    // 3  LocalReset
	"UR",    // 4  UpstreamRemoteReset
	"UF",    // 5  UpstreamConnectionFailure
	"UC",    // 6  UpstreamConnectionTermination
	"UO",    // 7  UpstreamOverflow
	"NR",    // 8  NoRouteFound
	"DI",    // 9  DelayInjected
	"FI",    // 10 FaultInjected
	"RL",    // 11 RateLimited
	"UAEX",  // 12 UnauthorizedExternalService
	"RLSE",  // 13 RateLimitServiceError
	"DC",    // 14 DownstreamConnectionTermination
	"URX",   // 15 UpstreamRetryLimitExceeded
	"SI",    // 16 StreamIdleTimeout
	"IH",    // 17 InvalidEnvoyRequestHeaders
	"DPE",   // 18 DownstreamProtocolError
	"UMSDR", // 19 UpstreamMaxStreamDurationReached
	"RFCF",  // 20 ResponseFromCacheFilter
	"NFCF",  // 21 NoFilterConfigFound
	"DT",    // 22 DurationTimeout
	"UPE",   // 23 UpstreamProtocolError
	"NC",    // 24 NoClusterFound
	"OM",    // 25 OverloadManager
}

// responseFlags converts the access logger uint64 bitmask to Envoy's
// human-readable flag string (e.g. "UF,UH,UT"). Bit positions match
// CoreResponseFlag enum in abi.h.
func responseFlags(mask uint64) string {
	if mask == 0 {
		return ""
	}
	var out []string
	for i, name := range responseFlagNames {
		if mask&(1<<uint(i)) != 0 {
			out = append(out, name)
		}
	}
	for i := len(responseFlagNames); i < 64; i++ {
		if mask&(1<<uint(i)) != 0 {
			out = append(out, fmt.Sprintf("0x%x", uint64(1)<<uint(i)))
		}
	}
	return strings.Join(out, ",")
}

// buildMinimalRecord constructs a record for requests where the response handler
// never fired (e.g. client disconnect before upstream responded). All fields
// that require response headers are left empty.
func (l *alLogger) buildMinimalRecord(h shared.AccessLoggerHandle, requestID string) *sink.Record {
	r := &sink.Record{RequestID: requestID}
	// Request attributes via header map (attribute IDs may not be available in access logger context).
	if v, ok := h.GetHeader(shared.HttpHeaderTypeRequest, ":method"); ok {
		r.Method = v.ToString()
	}
	if v, ok := h.GetHeader(shared.HttpHeaderTypeRequest, ":path"); ok {
		r.Path = v.ToString()
	}
	if v, ok := h.GetHeader(shared.HttpHeaderTypeRequest, ":authority"); ok {
		r.Host = v.ToString()
	}
	if v, ok := h.GetAttributeString(shared.AttributeIDUpstreamAddress); ok {
		r.UpstreamAddress = v.ToString()
	}
	return r
}
