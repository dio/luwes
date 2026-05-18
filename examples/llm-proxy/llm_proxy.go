// Package llmproxy is a zero-allocation LLM proxy filter for Envoy dynamic modules.
//
// Hot path (per-request):
//   - Reads "model" field from the JSON request body via a zero-alloc byte scan.
//   - Maps the model name to an Envoy cluster (openai, anthropic, default) via
//     a static prefix table: no map lookup, no allocation.
//   - Sets x-cluster request header and calls ClearRouteCache so Envoy's
//     cluster_header route selects the right upstream.
//   - On the response side, writes SSE chunks into a HeadTail ring buffer and
//     extracts token usage on stream completion without buffering the full body.
//
// Alloc profile on real Envoy (flamegraph baseline):
//   - getSingleHeader CGO escape: 1 alloc per GetOneInto call (ABI-level, unavoidable)
//   - GetChunks: 2 allocs per OnResponseBody call (ABI-level)
//   - Filter struct: 0 allocs (pooled in Factory.pool)
//   - scanModel + resolveCluster: 0 allocs (verified by AllocsPerRun)
//
// # Envoy config
//
// Requires a cluster_header route and three upstream clusters:
//
//	route_config:
//	  virtual_hosts:
//	    - name: providers
//	      routes:
//	        - match: { prefix: "/" }
//	          route:
//	            cluster_header: x-cluster
//
// See envoy.yaml for a complete working example.
package llmproxy

import (
	"strings"
	"sync"

	"github.com/dio/luwes/buffer"
	"github.com/dio/luwes/shared"
)

// Filter is a per-request instance, pooled by Factory.
// Pool reuse eliminates the filter struct alloc on every request.
type Filter struct {
	shared.EmptyHttpFilter

	handle  shared.HttpFilterHandle
	factory *Factory

	// ring captures SSE chunks for token extraction.
	// Allocated once per filter instance (pool reuse, not per-request).
	ring *buffer.HeadTail

	// cluster is set in OnRequestBody, read in OnStreamComplete.
	// Points into a string constant: no allocation.
	cluster string

	// skipSSE is set in OnResponseHeaders when Content-Type is not SSE.
	skipSSE bool
}

// Factory is created once per filter config (one per Envoy listener).
// Holds metric IDs and a pool of Filter instances.
type Factory struct {
	inputTokens   shared.MetricID
	outputTokens  shared.MetricID
	requestsTotal shared.MetricID

	pool sync.Pool
}

// NewFactory parses config and defines metrics. Called once at Envoy config load.
func NewFactory(h shared.HttpFilterConfigHandle, _ []byte) (shared.HttpFilterFactory, error) {
	f := &Factory{}
	f.pool.New = func() any {
		return &Filter{
			factory: f,
			// Pre-allocate the ring once per filter instance.
			// 8 KB head captures message_start / early usage.
			// 64 KB tail captures message_delta / final usage chunk.
			ring: buffer.NewHeadTail(8*1024, 64*1024),
		}
	}
	if h != nil {
		var res shared.MetricsResult
		f.requestsTotal, res = h.DefineCounter("llm_proxy_requests_total", "cluster")
		if res != shared.MetricsSuccess {
			f.requestsTotal = 0
		}
		f.inputTokens, res = h.DefineCounter("llm_proxy_input_tokens", "cluster")
		if res != shared.MetricsSuccess {
			f.inputTokens = 0
		}
		f.outputTokens, res = h.DefineCounter("llm_proxy_output_tokens", "cluster")
		if res != shared.MetricsSuccess {
			f.outputTokens = 0
		}
	}
	return f, nil
}

// Create returns a pooled Filter for the request.
func (f *Factory) Create(handle shared.HttpFilterHandle) shared.HttpFilter {
	filter := f.pool.Get().(*Filter)
	filter.handle = handle
	filter.cluster = clusterDefault
	filter.skipSSE = false
	filter.ring.Reset()
	return filter
}

func (f *Factory) OnDestroy() {}

// OnRequestHeaders signals Envoy to buffer the request body.
// The actual handler logic runs in OnRequestBody once the body is complete.
func (f *Filter) OnRequestHeaders(_ shared.HeaderMap, _ bool) shared.HeadersStatus {
	return shared.HeadersStatusStopAllAndBuffer
}

// OnRequestBody runs when the full request body is available (endStream=true).
// Scans for "model", resolves cluster, sets x-cluster, clears route cache.
// All operations are zero-alloc.
func (f *Filter) OnRequestBody(body shared.BodyBuffer, endStream bool) shared.BodyStatus {
	if !endStream {
		return shared.BodyStatusStopAndBuffer
	}

	var bodyBuf shared.UnsafeEnvoyBuffer
	chunks := body.GetChunks()
	if len(chunks) > 0 {
		bodyBuf = chunks[0]
	}

	model := modelFromBody(bodyBuf.ToUnsafeBytes())
	cluster := resolveCluster(model)

	f.cluster = cluster
	f.handle.RequestHeaders().Set("x-cluster", cluster)
	f.handle.ClearRouteCache()

	if f.factory.requestsTotal != 0 {
		f.handle.IncrementCounterValue(f.factory.requestsTotal, 1, cluster)
	}

	return shared.BodyStatusContinue
}

// OnResponseHeaders checks Content-Type to decide whether to tap the SSE stream.
func (f *Filter) OnResponseHeaders(headers shared.HeaderMap, _ bool) shared.HeadersStatus {
	var ctBuf shared.UnsafeEnvoyBuffer
	if headers.GetOneInto("content-type", &ctBuf) {
		if !strings.Contains(ctBuf.ToUnsafeString(), "text/event-stream") {
			f.skipSSE = true
		}
	} else {
		f.skipSSE = true
	}
	return shared.HeadersStatusContinue
}

// OnResponseBody taps SSE chunks into the ring buffer.
// On endStream, extracts token usage and emits counters.
func (f *Filter) OnResponseBody(body shared.BodyBuffer, endStream bool) shared.BodyStatus {
	if f.skipSSE {
		return shared.BodyStatusContinue
	}

	chunks := body.GetChunks()
	for _, chunk := range chunks {
		if chunk.Len > 0 {
			f.ring.Write(chunk.ToUnsafeBytes())
		}
	}

	if !endStream {
		return shared.BodyStatusContinue
	}

	// Import SSE parsing from the sse-tap example package.
	usage := extractUsage(f.ring.Head(), f.ring.Tail())
	f.ring.Reset()

	if usage.Input > 0 && f.factory.inputTokens != 0 {
		f.handle.IncrementCounterValue(f.factory.inputTokens, uint64(usage.Input), f.cluster)
	}
	if usage.Output > 0 && f.factory.outputTokens != 0 {
		f.handle.IncrementCounterValue(f.factory.outputTokens, uint64(usage.Output), f.cluster)
	}

	return shared.BodyStatusContinue
}

func (f *Filter) OnStreamComplete() {
	f.handle = nil
	f.factory.pool.Put(f)
}
