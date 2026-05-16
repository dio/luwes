// Package headerauth demonstrates the simplest possible luwes filter.
// It reads one request header (x-api-key), rejects if absent, injects
// x-user-id if present. No body reading, no response phase.
//
// This is the benchmark baseline: a correctly written header-only filter
// should show 0 allocs/op on the hot path with luwes.
package headerauth

import (
	"github.com/dio/luwes/shared"
)

// Filter is a per-request filter instance.
type Filter struct {
	shared.EmptyHttpFilter
	handle shared.HttpFilterHandle
}

// Factory is created once per filter config. Holds the counter metric ID.
type Factory struct {
	counter shared.MetricID
}

// NewFactory parses config and defines metrics. Called once at Envoy config load.
func NewFactory(h shared.HttpFilterConfigHandle, _ []byte) (*Factory, error) {
	id, res := h.DefineCounter("header_auth_requests_total", "result")
	if res != shared.MetricsSuccess {
		return nil, nil // non-fatal: metrics unavailable in test/fake env
	}
	return &Factory{counter: id}, nil
}

// Create returns a new filter for each request.
func (f *Factory) Create(handle shared.HttpFilterHandle) shared.HttpFilter {
	return &Filter{handle: handle}
}

func (f *Factory) OnDestroy() {}

// OnRequestHeaders is the hot path. GetOne, no Get, no GetAll.
func (f *Filter) OnRequestHeaders(headers shared.HeaderMap, _ bool) shared.HeadersStatus {
	key := headers.GetOne("x-api-key")
	if key.Ptr == nil || key.Len == 0 {
		f.handle.SendLocalResponse(401, nil, []byte(`{"error":"missing x-api-key"}`), "auth")
		return shared.HeadersStatusStop
	}
	// Inject user identity header using the unsafe string -- valid for the
	// duration of this callback since we call SetRequestHeader immediately.
	f.handle.RequestHeaders().Set("x-user-id", key.ToUnsafeString())
	return shared.HeadersStatusContinue
}

func (f *Filter) OnRequestBody(_ shared.BodyBuffer, _ bool) shared.BodyStatus {
	return shared.BodyStatusDefault
}
func (f *Filter) OnRequestTrailers(_ shared.HeaderMap) shared.TrailersStatus {
	return shared.TrailersStatusDefault
}
func (f *Filter) OnResponseHeaders(_ shared.HeaderMap, _ bool) shared.HeadersStatus {
	return shared.HeadersStatusDefault
}
func (f *Filter) OnResponseBody(_ shared.BodyBuffer, _ bool) shared.BodyStatus {
	return shared.BodyStatusDefault
}
func (f *Filter) OnResponseTrailers(_ shared.HeaderMap) shared.TrailersStatus {
	return shared.TrailersStatusDefault
}
func (f *Filter) OnStreamComplete() {}
