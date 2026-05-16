// Package headerauth demonstrates the simplest possible luwes filter.
// It reads one request header (x-api-key), rejects if absent, injects
// x-user-id if present. No body reading, no response phase.
//
// This is the benchmark baseline: a correctly written header-only filter
// should show 0 allocs/op on the hot path with luwes.
package headerauth

import (
	"sync"

	"github.com/dio/luwes/shared"
)

// Filter is a per-request filter instance.
type Filter struct {
	shared.EmptyHttpFilter
	handle  shared.HttpFilterHandle
	factory *Factory // back-pointer to return to pool
}

// Factory is created once per filter config. Holds the counter metric ID
// and a pool of Filter instances.
type Factory struct {
	counter shared.MetricID
	pool    sync.Pool
}

// NewFactory parses config and defines metrics. Called once at Envoy config load.
func NewFactory(h shared.HttpFilterConfigHandle, _ []byte) (shared.HttpFilterFactory, error) {
	f := &Factory{}
	f.pool.New = func() any { return &Filter{factory: f} }
	if h != nil {
		id, res := h.DefineCounter("header_auth_requests_total", "result")
		if res == shared.MetricsSuccess {
			f.counter = id
		}
	}
	return f, nil
}

// Create returns a pooled filter for the request.
func (f *Factory) Create(handle shared.HttpFilterHandle) shared.HttpFilter {
	filter := f.pool.Get().(*Filter)
	filter.handle = handle
	return filter
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
func (f *Filter) OnStreamComplete() {
	f.handle = nil
	f.factory.pool.Put(f)
}
