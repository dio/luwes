package fake

import (
	"github.com/dio/luwes/shared"
)

// FilterHandleOption configures a FakeFilterHandle.
type FilterHandleOption func(*FakeFilterHandle)

// WithHeaders sets the request headers on the fake handle.
func WithHeaders(headers map[string]string) FilterHandleOption {
	return func(h *FakeFilterHandle) {
		h.reqHeaders = NewFakeHeaderMap(headers)
	}
}

// WithResponseHeaders sets the response headers on the fake handle.
func WithResponseHeaders(headers map[string]string) FilterHandleOption {
	return func(h *FakeFilterHandle) {
		h.respHeaders = NewFakeHeaderMap(headers)
	}
}

// WithRequestBody sets the request body on the fake handle.
func WithRequestBody(body []byte) FilterHandleOption {
	return func(h *FakeFilterHandle) {
		h.reqBody = NewFakeBodyBuffer(body)
	}
}

// WithResponseBody sets the response body on the fake handle.
func WithResponseBody(body []byte) FilterHandleOption {
	return func(h *FakeFilterHandle) {
		h.respBody = NewFakeBodyBuffer(body)
	}
}

// NewFilterHandle constructs a FakeFilterHandle with the given options.
func NewFilterHandle(opts ...FilterHandleOption) *FakeFilterHandle {
	h := &FakeFilterHandle{
		reqHeaders:  NewFakeHeaderMap(nil),
		respHeaders: NewFakeHeaderMap(nil),
		reqBody:     NewFakeBodyBuffer(nil),
		respBody:    NewFakeBodyBuffer(nil),
		metadata:    make(map[string]map[string]any),
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

// FakeFilterHandle implements shared.HttpFilterHandle for unit tests and benchmarks.
// It records mutations so tests can assert on side effects.
//
// Not all methods are implemented -- only those needed for the hot path.
// Methods that would require a real Envoy scheduler are no-ops; tests that
// need async behaviour should use the real e2e test suite.
type FakeFilterHandle struct {
	reqHeaders  *FakeHeaderMap
	respHeaders *FakeHeaderMap
	reqBody     *FakeBodyBuffer
	respBody    *FakeBodyBuffer
	metadata    map[string]map[string]any

	// Recorded side effects for assertions.
	LocalResponses []LocalResponse
	ContinuedReq   int
	ContinuedResp  int
}

type LocalResponse struct {
	Status  uint32
	Headers [][2]string
	Body    []byte
	Detail  string
}

// -- HeaderMap accessors --

func (h *FakeFilterHandle) RequestHeaders() shared.HeaderMap  { return h.reqHeaders }
func (h *FakeFilterHandle) ResponseHeaders() shared.HeaderMap { return h.respHeaders }
func (h *FakeFilterHandle) RequestTrailers() shared.HeaderMap {
	return NewFakeHeaderMap(nil)
}
func (h *FakeFilterHandle) ResponseTrailers() shared.HeaderMap {
	return NewFakeHeaderMap(nil)
}

// -- Body accessors --

func (h *FakeFilterHandle) BufferedRequestBody() shared.BodyBuffer  { return h.reqBody }
func (h *FakeFilterHandle) ReceivedRequestBody() shared.BodyBuffer  { return h.reqBody }
func (h *FakeFilterHandle) BufferedResponseBody() shared.BodyBuffer { return h.respBody }
func (h *FakeFilterHandle) ReceivedResponseBody() shared.BodyBuffer { return h.respBody }
func (h *FakeFilterHandle) ReceivedBufferedRequestBody() bool       { return false }
func (h *FakeFilterHandle) ReceivedBufferedResponseBody() bool      { return false }

// -- Flow control --

func (h *FakeFilterHandle) ContinueRequest()     { h.ContinuedReq++ }
func (h *FakeFilterHandle) ContinueResponse()    { h.ContinuedResp++ }
func (h *FakeFilterHandle) ClearRouteCache()     {}
func (h *FakeFilterHandle) RefreshRouteCluster() {}

// -- Local response --

func (h *FakeFilterHandle) SendLocalResponse(status uint32, headers [][2]string, body []byte, detail string) {
	h.LocalResponses = append(h.LocalResponses, LocalResponse{status, headers, body, detail})
}

func (h *FakeFilterHandle) SendResponseHeaders(headers [][2]string, eos bool) {}
func (h *FakeFilterHandle) SendResponseData(body []byte, eos bool)            {}
func (h *FakeFilterHandle) SendResponseTrailers(trailers [][2]string)         {}

// -- Metadata --

func (h *FakeFilterHandle) SetMetadata(ns, key string, value any) {
	if h.metadata[ns] == nil {
		h.metadata[ns] = make(map[string]any)
	}
	h.metadata[ns][key] = value
}

func (h *FakeFilterHandle) GetMetadataString(source shared.MetadataSourceType, ns, key string) (shared.UnsafeEnvoyBuffer, bool) {
	if v, ok := h.metadata[ns][key]; ok {
		if s, ok := v.(string); ok {
			return shared.UnsafeEnvoyBuffer{Ptr: &[]byte(s)[0], Len: uint64(len(s))}, true
		}
	}
	return shared.UnsafeEnvoyBuffer{}, false
}

func (h *FakeFilterHandle) GetMetadataNumber(_ shared.MetadataSourceType, ns, key string) (float64, bool) {
	if v, ok := h.metadata[ns][key]; ok {
		if f, ok := v.(float64); ok {
			return f, true
		}
	}
	return 0, false
}

func (h *FakeFilterHandle) GetMetadataBool(_ shared.MetadataSourceType, ns, key string) (bool, bool) {
	if v, ok := h.metadata[ns][key]; ok {
		if b, ok := v.(bool); ok {
			return b, true
		}
	}
	return false, false
}

func (h *FakeFilterHandle) GetMetadataKeys(_ shared.MetadataSourceType, _ string) []shared.UnsafeEnvoyBuffer {
	return nil
}
func (h *FakeFilterHandle) GetMetadataNamespaces(_ shared.MetadataSourceType) []shared.UnsafeEnvoyBuffer {
	return nil
}
func (h *FakeFilterHandle) AddMetadataListNumber(_, _ string, _ float64) bool { return false }
func (h *FakeFilterHandle) AddMetadataListString(_, _, _ string) bool         { return false }
func (h *FakeFilterHandle) AddMetadataListBool(_, _ string, _ bool) bool      { return false }
func (h *FakeFilterHandle) GetMetadataListSize(_ shared.MetadataSourceType, _, _ string) (int, bool) {
	return 0, false
}
func (h *FakeFilterHandle) GetMetadataListNumber(_ shared.MetadataSourceType, _, _ string, _ int) (float64, bool) {
	return 0, false
}
func (h *FakeFilterHandle) GetMetadataListString(_ shared.MetadataSourceType, _, _ string, _ int) (shared.UnsafeEnvoyBuffer, bool) {
	return shared.UnsafeEnvoyBuffer{}, false
}
func (h *FakeFilterHandle) GetMetadataListBool(_ shared.MetadataSourceType, _, _ string, _ int) (bool, bool) {
	return false, false
}

// -- Attributes --

func (h *FakeFilterHandle) GetAttributeString(_ shared.AttributeID) (shared.UnsafeEnvoyBuffer, bool) {
	return shared.UnsafeEnvoyBuffer{}, false
}
func (h *FakeFilterHandle) GetAttributeNumber(_ shared.AttributeID) (float64, bool) { return 0, false }
func (h *FakeFilterHandle) GetAttributeBool(_ shared.AttributeID) (bool, bool)      { return false, false }

// -- Filter state --

func (h *FakeFilterHandle) GetFilterState(_ string) (shared.UnsafeEnvoyBuffer, bool) {
	return shared.UnsafeEnvoyBuffer{}, false
}
func (h *FakeFilterHandle) SetFilterState(key string, value []byte)           {}
func (h *FakeFilterHandle) SetFilterStateTyped(key string, value []byte) bool { return false }
func (h *FakeFilterHandle) GetFilterStateTyped(_ string) (shared.UnsafeEnvoyBuffer, bool) {
	return shared.UnsafeEnvoyBuffer{}, false
}

// -- Cross-phase data --

func (h *FakeFilterHandle) GetData(_ string) any       { return nil }
func (h *FakeFilterHandle) SetData(_ string, _ any)    {}
func (h *FakeFilterHandle) GetMostSpecificConfig() any { return nil }

// -- Logging --

func (h *FakeFilterHandle) Log(_ shared.LogLevel, _ string, _ ...any) {}
func (h *FakeFilterHandle) LogEnabled(_ shared.LogLevel) bool         { return false }

// -- Scheduler (no-op -- use e2e tests for async behaviour) --

func (h *FakeFilterHandle) GetScheduler() shared.Scheduler { return &fakeScheduler{} }

type fakeScheduler struct{}

func (s *fakeScheduler) Schedule(fn func()) { fn() } // synchronous in tests

// -- HTTP callout / stream (no-op) --

func (h *FakeFilterHandle) HttpCallout(_ string, _ [][2]string, _ []byte, _ uint64, _ shared.HttpCalloutCallback) (shared.HttpCalloutInitResult, uint64) {
	return shared.HttpCalloutInitClusterNotFound, 0
}
func (h *FakeFilterHandle) StartHttpStream(_ string, _ [][2]string, _ []byte, _ bool, _ uint64, _ shared.HttpStreamCallback) (shared.HttpCalloutInitResult, uint64) {
	return shared.HttpCalloutInitClusterNotFound, 0
}
func (h *FakeFilterHandle) SendHttpStreamData(_ uint64, _ []byte, _ bool) bool  { return false }
func (h *FakeFilterHandle) SendHttpStreamTrailers(_ uint64, _ [][2]string) bool { return false }
func (h *FakeFilterHandle) ResetHttpStream(_ uint64)                            {}

// -- Watermarks --

func (h *FakeFilterHandle) SetDownstreamWatermarkCallbacks(_ shared.DownstreamWatermarkCallbacks) {}
func (h *FakeFilterHandle) ClearDownstreamWatermarkCallbacks()                                    {}

// -- Metrics --

func (h *FakeFilterHandle) RecordHistogramValue(_ shared.MetricID, _ uint64, _ ...string) shared.MetricsResult {
	return shared.MetricsSuccess
}
func (h *FakeFilterHandle) SetGaugeValue(_ shared.MetricID, _ uint64, _ ...string) shared.MetricsResult {
	return shared.MetricsSuccess
}
func (h *FakeFilterHandle) IncrementGaugeValue(_ shared.MetricID, _ uint64, _ ...string) shared.MetricsResult {
	return shared.MetricsSuccess
}
func (h *FakeFilterHandle) DecrementGaugeValue(_ shared.MetricID, _ uint64, _ ...string) shared.MetricsResult {
	return shared.MetricsSuccess
}
func (h *FakeFilterHandle) IncrementCounterValue(_ shared.MetricID, _ uint64, _ ...string) shared.MetricsResult {
	return shared.MetricsSuccess
}

// -- Misc --

func (h *FakeFilterHandle) AddCustomFlag(_ string)     {}
func (h *FakeFilterHandle) GetWorkerIndex() uint32     { return 0 }
func (h *FakeFilterHandle) GetBufferLimit() uint64     { return 0 }
func (h *FakeFilterHandle) SetBufferLimit(_ uint64)    {}
func (h *FakeFilterHandle) GetActiveSpan() shared.Span { return nil }
func (h *FakeFilterHandle) GetClusterName() (shared.UnsafeEnvoyBuffer, bool) {
	return shared.UnsafeEnvoyBuffer{}, false
}
func (h *FakeFilterHandle) GetClusterHostCounts(_ uint32) (shared.ClusterHostCounts, bool) {
	return shared.ClusterHostCounts{}, false
}
func (h *FakeFilterHandle) SetUpstreamOverrideHost(_ string, _ bool) bool              { return false }
func (h *FakeFilterHandle) ResetStream(_ shared.HttpFilterStreamResetReason, _ string) {}
func (h *FakeFilterHandle) SendGoAwayAndClose(_ bool)                                  {}
func (h *FakeFilterHandle) RecreateStream(_ [][2]string) bool                          { return false }
func (h *FakeFilterHandle) SetSocketOptionInt(_, _ int64, _ shared.SocketOptionState, _ shared.SocketDirection, _ int64) bool {
	return false
}
func (h *FakeFilterHandle) SetSocketOptionBytes(_, _ int64, _ shared.SocketOptionState, _ shared.SocketDirection, _ []byte) bool {
	return false
}
func (h *FakeFilterHandle) GetSocketOptionInt(_, _ int64, _ shared.SocketOptionState, _ shared.SocketDirection) (int64, bool) {
	return 0, false
}
func (h *FakeFilterHandle) GetSocketOptionBytes(_, _ int64, _ shared.SocketOptionState, _ shared.SocketDirection) (shared.UnsafeEnvoyBuffer, bool) {
	return shared.UnsafeEnvoyBuffer{}, false
}
