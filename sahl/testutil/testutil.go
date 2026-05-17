// Package sahltest provides test helpers for sahl filters and their examples.
//
// Import only from _test.go files. Do not use in production code.
//
//	import "github.com/dio/luwes/sahl/testutil"
//
// The central type is [Filter], which wraps a sahl filter instance and exposes
// the full Envoy HTTP filter lifecycle as typed methods. Use it to drive request
// and response phases without a real Envoy process.
//
// Typical usage:
//
//	fh := fake.NewFilterHandle(fake.WithHeaders(...), fake.WithResponseHeaders(...))
//	f := testutil.NewFilter("my-filter", myHandler, nil, false, fh)
//	f.OnRequestHeaders(fh.RequestHeaders(), false)
//	f.OnResponseHeaders(fh.ResponseHeaders(), false)
//	f.OnResponseBody(fh.BufferedResponseBody(), true)
//	f.OnStreamComplete()
//	f.OnDestroy()
package sahltest

import (
	"github.com/dio/luwes/sahl"
	"github.com/dio/luwes/shared"
)

// Filter wraps a sahl filter instance and exposes its lifecycle methods with
// concrete types. Returned by [NewFilter], [NewFilterWithResponse], and
// [NewBodyAwareFilter].
type Filter struct {
	f *sahl.SahlFilterForTesting
}

// NewFilter constructs a synchronous request-only filter for tests.
// resp is nil; bodyAware is false.
func NewFilter(
	name string,
	handler sahl.HandlerFunc,
	handle shared.HttpFilterHandle,
) *Filter {
	return &Filter{f: sahl.NewFilterForTesting(name, handler, nil, false, handle)}
}

// NewFilterWithResponse constructs a filter with a response observer for tests.
// Use this to exercise onResponseHeaders, onResponseBody, and OnStreamComplete
// flush paths.
func NewFilterWithResponse(
	name string,
	handler sahl.HandlerFunc,
	resp sahl.ResponseHandlerFunc,
	handle shared.HttpFilterHandle,
) *Filter {
	return &Filter{f: sahl.NewFilterForTesting(name, handler, resp, false, handle)}
}

// NewBodyAwareFilter constructs a body-aware filter for tests.
// OnRequestHeaders returns HeadersStatusStopAllAndBuffer; the handler runs in
// OnRequestBody when endStream is true.
func NewBodyAwareFilter(
	name string,
	handler sahl.HandlerFunc,
	handle shared.HttpFilterHandle,
) *Filter {
	return &Filter{f: sahl.NewFilterForTesting(name, handler, nil, true, handle)}
}

// OnRequestHeaders drives the request headers phase.
func (f *Filter) OnRequestHeaders(headers shared.HeaderMap, endStream bool) shared.HeadersStatus {
	return f.f.OnRequestHeaders(headers, endStream)
}

// OnRequestBody drives the request body phase.
func (f *Filter) OnRequestBody(body shared.BodyBuffer, endStream bool) shared.BodyStatus {
	return f.f.OnRequestBody(body, endStream)
}

// OnResponseHeaders drives the response headers phase.
func (f *Filter) OnResponseHeaders(headers shared.HeaderMap, endStream bool) shared.HeadersStatus {
	return f.f.OnResponseHeaders(headers, endStream)
}

// OnResponseBody drives the response body phase.
func (f *Filter) OnResponseBody(body shared.BodyBuffer, endStream bool) shared.BodyStatus {
	return f.f.OnResponseBody(body, endStream)
}

// OnStreamComplete signals stream completion, triggering response mutation flush.
func (f *Filter) OnStreamComplete() {
	f.f.OnStreamComplete()
}

// OnDestroy signals filter destruction, returning the filter to the pool.
func (f *Filter) OnDestroy() {
	f.f.OnDestroy()
}
