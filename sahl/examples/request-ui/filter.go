// Package requestui is a sahl filter that records every request and response
// into a Postgres database and serves a near-realtime web UI.
//
// Per-request state is initialized in the response handler's headers call
// (when chunk.StatusCode != 0) and accumulated through body chunks until
// EndStream. Request-side fields (method, path, host, request headers)
// that are readable in the response path come from attributes on the Writer.
// The ResponseChunk.Context slot carries per-request state across callbacks.
package requestui

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/dio/luwes/sahl"
	requestuisink "github.com/dio/luwes/sahl/examples/request-ui/sink"
	"github.com/dio/luwes/shared"
)

const defaultMaxBody = 4096

var (
	cfgMu sync.RWMutex
	cfg   = filterConfig{
		RecordRequestHeaders:  true,
		RecordResponseHeaders: true,
		MaxBodyBytes:          defaultMaxBody,
	}
)

type filterConfig struct {
	RecordRequestHeaders  bool `json:"record_request_headers"`
	RecordResponseHeaders bool `json:"record_response_headers"`
	RecordRequestBody     bool `json:"record_request_body"`
	RecordResponseBody    bool `json:"record_response_body"`
	MaxBodyBytes          int  `json:"max_body_bytes"`
}

// reqState is the per-request accumulated data stored in ResponseChunk.Context.
type reqState struct {
	requestID      string
	method         string
	path           string
	host           string
	traceID        string
	spanID         string
	requestHeaders [][2]string
	requestBody    string // retrieved from bodyCache if RecordRequestBody

	upstreamAddress string
	responseHeaders [][2]string
	responseBody    string
	errorDetails    string
}

var statePool = sync.Pool{New: func() any { return &reqState{} }}

// Register wires the filter into the sahl registry.
// Call from init() in cmd/main.go after constructing the sink.
func Register(name string, s *requestuisink.Sink, pending *PendingRecords) {
	sahl.RegisterWithConfigAndResponse(
		name,
		func(h sahl.ConfigHandle) error {
			raw := h.RawConfig()
			if len(raw) == 0 {
				return nil
			}
			var c filterConfig
			if err := json.Unmarshal(raw, &c); err != nil {
				return err
			}
			if c.MaxBodyBytes <= 0 {
				c.MaxBodyBytes = defaultMaxBody
			}
			cfgMu.Lock()
			cfg = c
			cfgMu.Unlock()
			return nil
		},
		func(_ *sahl.Writer, _ *sahl.Request) {
			// Request body recording not supported in this variant.
			// Enable RecordRequestBody only when using RegisterWithBodyConfigAndResponse.
		},
		func(w *sahl.Writer, chunk *sahl.ResponseChunk) {
			cfgMu.RLock()
			c := cfg
			cfgMu.RUnlock()

			// Headers call: StatusCode != 0, Data == nil.
			// All metadata is available here via attributes. Emit immediately
			// unless RecordResponseBody is on -- in that case stash state and
			// emit on EndStream so we can include the body.
			if chunk.StatusCode != 0 {
				st := statePool.Get().(*reqState)
				*st = reqState{}

				if v, ok := w.GetAttributeString(shared.AttributeIDRequestId); ok {
					st.requestID = v.ToString()
				}
				if v, ok := w.GetAttributeString(shared.AttributeIDRequestMethod); ok {
					st.method = v.ToString()
				}
				if v, ok := w.GetAttributeString(shared.AttributeIDRequestPath); ok {
					st.path = v.ToString()
				}
				if v, ok := w.GetAttributeString(shared.AttributeIDRequestHost); ok {
					st.host = v.ToString()
				}
				if span := w.ActiveSpan(); span != nil {
					if id, ok := span.GetTraceID(); ok {
						st.traceID = id.ToString()
					}
					if id, ok := span.GetSpanID(); ok {
						st.spanID = id.ToString()
					}
				}
				if addr, ok := w.GetAttributeString(shared.AttributeIDUpstreamAddress); ok {
					st.upstreamAddress = addr.ToString()
				}
				// Capture Envoy-generated local reply details (upstream timeout,
				// circuit breaker, rate limit). Non-empty only when Envoy
				// synthesized the response rather than forwarding from upstream.
				if d := w.LocalReplyDetails(); d != "" {
					st.errorDetails = d
				}
				if c.RecordResponseHeaders && chunk.Headers != nil {
					st.responseHeaders = copyHeaders(chunk.Headers.GetAll())
				}

				if c.RecordResponseBody {
					// Defer emit until body is accumulated in the body path.
					*chunk.Context = st
					return
				}
				// Deposit partial record; the access logger enriches and sends it.
				nr := buildRecord(chunk.StatusCode, st)
				pending.Store(st.requestID, nr)
				*st = reqState{}
				statePool.Put(st)
				return
			}

			// Body call -- only reached when RecordResponseBody is true.
			if *chunk.Context == nil {
				return
			}
			st, ok := (*chunk.Context).(*reqState)
			if !ok || st == nil {
				return
			}
			if chunk.EndStream {
				if len(chunk.Data) > 0 {
					data := chunk.Data
					if len(data) > c.MaxBodyBytes {
						data = data[:c.MaxBodyBytes]
					}
					st.responseBody = string(data)
				}
				nr := buildRecord(chunk.StatusCode, st)
				pending.Store(st.requestID, nr)
				*st = reqState{}
				statePool.Put(st)
			}
		},
	)
}

// buildRecord constructs a partial Record from state available at response headers time.
// Finalized fields (duration, byte counts, flags, code_details) are left zero;
// the access logger sets them in OnLog after stream finalization.
func buildRecord(statusCode int, st *reqState) *requestuisink.Record {
	r := &requestuisink.Record{
		RequestID:       st.requestID,
		Method:          st.method,
		Path:            st.path,
		Host:            st.host,
		TraceID:         st.traceID,
		SpanID:          st.spanID,
		RequestBody:     st.requestBody,
		UpstreamAddress: st.upstreamAddress,
		ResponseBody:    st.responseBody,
		ErrorDetails:    st.errorDetails,
		ResponseCode:    float64(statusCode),
		UpstreamStatus:  statusStr(statusCode),
	}
	if len(st.requestHeaders) > 0 {
		if b, err := json.Marshal(st.requestHeaders); err == nil {
			r.RequestHeaders = string(b)
		}
	}
	if len(st.responseHeaders) > 0 {
		if b, err := json.Marshal(st.responseHeaders); err == nil {
			r.ResponseHeaders = string(b)
		}
	}
	return r
}

func hasError(r *requestuisink.Record) bool {
	return r.ErrorDetails != "" ||
		r.UpstreamFailure != "" ||
		(r.ResponseFlags != "" && containsErrorFlag(r.ResponseFlags)) ||
		r.ResponseCode >= 500
}

func containsErrorFlag(flags string) bool {
	for _, f := range []string{"UF", "UH", "UC", "UT", "UO", "NR"} {
		if strings.Contains(flags, f) {
			return true
		}
	}
	return false
}

func statusStr(code int) string {
	if code == 0 {
		return ""
	}
	b := make([]byte, 0, 3)
	for code > 0 {
		b = append([]byte{byte(code%10) + '0'}, b...)
		code /= 10
	}
	return string(b)
}

func copyHeaders(raw [][2]shared.UnsafeEnvoyBuffer) [][2]string {
	out := make([][2]string, len(raw))
	for i, h := range raw {
		out[i] = [2]string{h[0].ToString(), h[1].ToString()}
	}
	return out
}
