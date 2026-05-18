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
	if flags := shared.ResponseFlagsString(h.GetResponseFlags()); flags != "" {
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
