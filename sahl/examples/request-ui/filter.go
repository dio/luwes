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
func Register(name string, s *requestuisink.Sink) {
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
				if c.RecordResponseHeaders && chunk.Headers != nil {
					st.responseHeaders = copyHeaders(chunk.Headers.GetAll())
				}

				if c.RecordResponseBody {
					// Defer emit until body is accumulated in the body path.
					*chunk.Context = st
					return
				}
				// No body recording: emit now and return state to pool.
				emit(w, chunk.StatusCode, st, s)
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
				emit(w, chunk.StatusCode, st, s)
				*st = reqState{}
				statePool.Put(st)
			}
		},
	)
}

func emit(w *sahl.Writer, statusCode int, st *reqState, s *requestuisink.Sink) {
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

	if v, ok := w.GetAttributeNumber(shared.AttributeIDRequestDuration); ok {
		r.DurationMs = v / 1e6
	}
	if v, ok := w.GetAttributeNumber(shared.AttributeIDRequestSize); ok {
		r.RequestSizeBytes = v
	}
	if v, ok := w.GetAttributeNumber(shared.AttributeIDResponseSize); ok {
		r.ResponseSizeBytes = v
	}
	if v, ok := w.GetAttributeNumber(shared.AttributeIDResponseCode); ok {
		r.ResponseCode = v
		r.UpstreamStatus = statusStr(int(v))
	} else {
		// AttributeIDResponseCode unavailable on macOS Envoy builds; use the
		// status code from the response headers callback instead.
		r.ResponseCode = float64(statusCode)
		r.UpstreamStatus = statusStr(statusCode)
	}
	if v, ok := w.GetAttributeString(shared.AttributeIDResponseFlags); ok && v.Len > 0 {
		r.ResponseFlags = v.ToString()
	}
	if v, ok := w.GetAttributeString(shared.AttributeIDResponseCodeDetails); ok && v.Len > 0 {
		r.ResponseCodeDetails = v.ToString()
	}
	if v, ok := w.GetAttributeString(shared.AttributeIDUpstreamTransportFailureReason); ok && v.Len > 0 {
		r.UpstreamFailure = v.ToString()
	}

	r.HasError = hasError(r)
	s.Send(r)
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
