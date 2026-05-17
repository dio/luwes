// Package hello is the simplest possible luwes filter.
//
// It reads the request :path header and stamps an x-hello response header
// with the value "from-luwes path=<path>". No config, no metrics, no body
// reading. The only purpose is to show the minimum viable filter structure.
//
// Build:
//
//	make build EXAMPLE=hello
//
// Test:
//
//	curl -v http://localhost:10000/
//	# expect: x-hello: from-luwes path=/
package hello

import "github.com/dio/luwes/shared"

// Filter handles one request. No pooling here; this is the reference
// implementation that shows the raw structure. See header-auth for pooling.
type Filter struct {
	shared.EmptyHttpFilter
	handle shared.HttpFilterHandle
	path   string
}

// Factory creates a Filter for each request. One instance per Envoy filter config.
type Factory struct{}

func (f *Factory) Create(handle shared.HttpFilterHandle) shared.HttpFilter {
	return &Filter{handle: handle}
}

func (f *Factory) OnDestroy() {}

// NewFactory satisfies the sdk.FactoryFunc signature. No config to parse.
func NewFactory(_ shared.HttpFilterConfigHandle, _ []byte) (shared.HttpFilterFactory, error) {
	return &Factory{}, nil
}

// OnRequestHeaders captures the path for use in the response phase.
// GetOne is zero-alloc. We store the unsafe string, valid for
// the duration of the callback and we copy it immediately into a field.
func (f *Filter) OnRequestHeaders(headers shared.HeaderMap, _ bool) shared.HeadersStatus {
	path := headers.GetOne(":path")
	// Copy into Go memory: the UnsafeEnvoyBuffer is only valid during this callback.
	f.path = path.ToString()
	return shared.HeadersStatusContinue
}

// OnResponseHeaders stamps the x-hello header. Called when Envoy has the
// response headers ready, the right phase for adding response headers.
func (f *Filter) OnResponseHeaders(headers shared.HeaderMap, _ bool) shared.HeadersStatus {
	headers.Set("x-hello", "from-luwes path="+f.path)
	return shared.HeadersStatusContinue
}
