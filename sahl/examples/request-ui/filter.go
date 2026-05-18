// Package requestui is a sahl filter that records every request and response
// into a Postgres database and serves a near-realtime web UI.
//
// Per-request state is initialized in the response handler's headers call
// (when chunk.StatusCode != 0) and accumulated through body chunks until
// EndStream. Request-side fields (method, path, host, request headers, body)
// that sahl gives us on the request side are stored in package-level vars
// scoped per worker -- but sahl runs everything on Envoy worker threads
// (single-threaded per request), so the ResponseChunk.Context slot is
// the correct mechanism: we initialize state there in the headers call.
//
// The one complication: request body (if RecordRequestBody=true) is only
// available in the request handler. We store it in a sync.Map keyed by
// x-request-id, then retrieve it in the response handler.
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
	// bodyCache stores request bodies keyed by x-request-id for hand-off
	// from the request handler to the response handler.
	// Entries are deleted after the response handler emits the record.
	bodyCache sync.Map
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
	sahl.RegisterWithBodyConfigAndResponse(
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
		func(w *sahl.Writer, r *sahl.Request) {
			cfgMu.RLock()
			c := cfg
			cfgMu.RUnlock()

			if c.RecordRequestBody {
				body := r.Body()
				if len(body) > c.MaxBodyBytes {
					body = body[:c.MaxBodyBytes]
				}
				if len(body) > 0 {
					reqID := r.Header.Get("x-request-id")
					if reqID != "" {
						bodyCache.Store(reqID, string(body))
					}
				}
			}
		},
		func(w *sahl.Writer, chunk *sahl.ResponseChunk) {
			cfgMu.RLock()
			c := cfg
			cfgMu.RUnlock()

			// Headers call: StatusCode != 0, Data == nil.
			// Initialize per-request state from request attributes.
			if chunk.StatusCode != 0 {
				st := statePool.Get().(*reqState)
				*st = reqState{}
				*chunk.Context = st

				// Read request identity from attributes -- available in response callbacks.
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

				if c.RecordRequestHeaders {
					// Request headers are no longer directly accessible here.
					// They are available via AttributeIDRequestHeaders as a serialized
					// string on some Envoy builds. For now we skip them unless a future
					// sahl version exposes the request HeaderMap in the response path.
				}

				// Retrieve request body stashed by the request handler.
				if c.RecordRequestBody && st.requestID != "" {
					if v, ok := bodyCache.LoadAndDelete(st.requestID); ok {
						st.requestBody, _ = v.(string)
					}
				}

				if addr, ok := w.GetAttributeString(shared.AttributeIDUpstreamAddress); ok {
					st.upstreamAddress = addr.ToString()
				}

				if c.RecordResponseHeaders && chunk.Headers != nil {
					st.responseHeaders = copyHeaders(chunk.Headers.GetAll())
				}
				return
			}

			// Body call.
			st, ok := (*chunk.Context).(*reqState)
			if !ok || st == nil {
				return
			}

			if c.RecordResponseBody && chunk.EndStream && len(chunk.Data) > 0 {
				data := chunk.Data
				if len(data) > c.MaxBodyBytes {
					data = data[:c.MaxBodyBytes]
				}
				st.responseBody = string(data)
			}

			if chunk.EndStream {
				emit(w, st, s)
				*st = reqState{}
				statePool.Put(st)
			}
		},
	)
}

func emit(w *sahl.Writer, st *reqState, s *requestuisink.Sink) {
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
