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

	requestuisink "github.com/dio/luwes/sahl/examples/request-ui/sink"
	"github.com/dio/luwes/shared"
)

// PendingRecords maps request ID to a partially filled Record waiting for
// finalized fields from the access logger. Exported so cmd/main.go can pass
// the same map to both the filter (via Register) and the access logger factory.
type PendingRecords struct {
	m sync.Map
}

// Store deposits a record for the access logger to consume.
func (p *PendingRecords) Store(requestID string, r *requestuisink.Record) {
	if requestID != "" {
		p.m.Store(requestID, r)
	}
}

// LoadAndDelete retrieves and removes a record by request ID.
func (p *PendingRecords) LoadAndDelete(requestID string) (*requestuisink.Record, bool) {
	val, ok := p.m.LoadAndDelete(requestID)
	if !ok {
		return nil, false
	}
	r, ok := val.(*requestuisink.Record)
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
	s *requestuisink.Sink,
) func(shared.AccessLoggerConfigHandle, []byte) (shared.AccessLoggerFactory, error) {
	return func(_ shared.AccessLoggerConfigHandle, _ []byte) (shared.AccessLoggerFactory, error) {
		return &alFactory{pending: pending, sink: s}, nil
	}
}

type alFactory struct {
	pending *PendingRecords
	sink    *requestuisink.Sink
}

func (f *alFactory) NewLogger() shared.AccessLogger {
	return &alLogger{pending: f.pending, sink: f.sink}
}
func (f *alFactory) OnDestroy() {}

type alLogger struct {
	shared.EmptyAccessLogger
	pending *PendingRecords
	sink    *requestuisink.Sink
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

	r.HasError = r.ErrorDetails != "" ||
		r.UpstreamFailure != "" ||
		(r.ResponseFlags != "" && containsErrorFlag(r.ResponseFlags)) ||
		r.ResponseCode >= 500

	l.sink.Send(r)
}

// responseFlags converts the access logger uint64 bitmask to Envoy's
// human-readable flag string (e.g. "UF,UH,UT"). Bit positions match
// CoreResponseFlag enum in abi.h.
func responseFlags(mask uint64) string {
	if mask == 0 {
		return ""
	}
	names := [...]string{
		"LH", "UH", "UT", "LR", "UR", "UF", "UC", "UO",
		"NR", "DI", "FI", "RL", "UAEX", "RLSE", "DC", "URX",
		"SI", "IH", "DPE", "UMSDR", "RFCF", "NFCF", "DT", "UPE",
		"NC", "OM",
	}
	var out []string
	for i, name := range names {
		if mask&(1<<uint(i)) != 0 {
			out = append(out, name)
		}
	}
	for i := len(names); i < 64; i++ {
		if mask&(1<<uint(i)) != 0 {
			out = append(out, fmt.Sprintf("0x%x", uint64(1)<<uint(i)))
		}
	}
	return strings.Join(out, ",")
}

// buildMinimalRecord constructs a record for requests where the response handler
// never fired (e.g. client disconnect before upstream responded). All fields
// that require response headers are left empty.
func (l *alLogger) buildMinimalRecord(h shared.AccessLoggerHandle, requestID string) *requestuisink.Record {
	r := &requestuisink.Record{RequestID: requestID}
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
