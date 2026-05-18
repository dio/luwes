// accesslogger.go: access logger half of the request-logger filter.
//
// The HTTP filter's OnStreamComplete deposits a partial record into
// Factory.pendingRecords, keyed by request ID. This access logger pops it,
// enriches it with finalized stream fields (duration, byte counts, response
// flags, code details) that are unavailable from HTTP filter callbacks, and
// emits the final log line.
//
// Envoy lifecycle guarantee: on_access_logger_log fires between
// OnStreamComplete and OnDestroy. OnDestroy removes any record the access
// logger did not consume, preventing leaks when no access logger is wired.
//
// Register from cmd/main.go:
//
//	luwes.Register("request-logger", requestlogger.NewFactory)
//	luwes.RegisterAccessLogger("request-logger", requestlogger.NewAccessLoggerFactory)
//	sdk.RegisterHttpFilterConfigFactories(luwes.Factories())
//	sdk.RegisterAccessLoggerConfigFactories(luwes.AccessLoggerFactories())
package requestlogger

import (
	"fmt"
	"strings"

	"github.com/dio/luwes/shared"
)

// NewAccessLoggerFactory returns an access logger factory that correlates with
// the HTTP filter via factory.pendingRecords. Pass the same Factory instance
// that was returned by NewFactory so both halves share the pending map.
func NewAccessLoggerFactory(f *Factory) func(shared.AccessLoggerConfigHandle, []byte) (shared.AccessLoggerFactory, error) {
	return func(_ shared.AccessLoggerConfigHandle, _ []byte) (shared.AccessLoggerFactory, error) {
		return &accessLoggerFactory{factory: f}, nil
	}
}

type accessLoggerFactory struct{ factory *Factory }

func (f *accessLoggerFactory) NewLogger() shared.AccessLogger {
	return &accessLogger{factory: f.factory}
}
func (f *accessLoggerFactory) OnDestroy() {}

type accessLogger struct {
	shared.EmptyAccessLogger
	factory *Factory
}

func (l *accessLogger) OnLog(h shared.AccessLoggerHandle, logType shared.AccessLogType) {
	// Only finalize on stream-end events; skip TCP periodic, upstream events, etc.
	if logType != shared.AccessLogTypeDownstreamEnd {
		return
	}

	// Correlate with the HTTP filter record via x-request-id.
	ridBuf, ok := h.GetHeader(shared.HttpHeaderTypeRequest, "x-request-id")
	if !ok {
		return
	}
	key := ridBuf.ToString()
	if key == "" {
		return
	}

	val, ok := l.factory.pendingRecords.LoadAndDelete(key)
	if !ok {
		return
	}
	r := val.(*record)

	// Enrich with finalized fields. These were unavailable in OnStreamComplete.
	timing := h.GetTimingInfo()
	if timing.RequestCompleteDurationNs >= 0 {
		r.durationMs = float64(timing.RequestCompleteDurationNs) / 1e6
	}
	b := h.GetBytesInfo()
	r.requestSizeBytes = float64(b.BytesReceived)
	r.responseSizeBytes = float64(b.BytesSent)

	if v, ok := h.GetAttributeInt(shared.AttributeIDResponseCode); ok && v > 0 {
		r.responseCode = float64(v)
	}
	if v, ok := h.GetAttributeString(shared.AttributeIDResponseCodeDetails); ok && v.Len > 0 {
		r.responseCodeDetails = v.ToString()
	}
	if flags := responseFlags(h.GetResponseFlags()); flags != "" {
		r.responseFlags = flags
	}
	if v, ok := h.GetAttributeString(shared.AttributeIDUpstreamTransportFailureReason); ok && v.Len > 0 {
		r.upstreamFailure = v.ToString()
	}

	emitRecord(h, r)
}

// emitRecord writes the final log line and dynamic metadata.
// Called from the access logger so the handle is an AccessLoggerHandle, not
// an HttpFilterHandle. Only the Log method is shared; SetMetadata is not
// available on AccessLoggerHandle, so metadata is skipped here.
func emitRecord(h shared.AccessLoggerHandle, r *record) {
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
