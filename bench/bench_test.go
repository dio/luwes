package bench

import (
	"testing"

	headerauth "github.com/dio/luwes/examples/header-auth"
	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
)

// fakeConfigHandle is a minimal HttpFilterConfigHandle for constructing factories.
type fakeConfigHandle struct{}

func (f *fakeConfigHandle) Log(_ shared.LogLevel, _ string, _ ...any) {}
func (f *fakeConfigHandle) DefineCounter(_ string, _ ...string) (shared.MetricID, shared.MetricsResult) {
	return 0, shared.MetricsSuccess
}
func (f *fakeConfigHandle) DefineGauge(_ string, _ ...string) (shared.MetricID, shared.MetricsResult) {
	return 0, shared.MetricsSuccess
}
func (f *fakeConfigHandle) DefineHistogram(_ string, _ ...string) (shared.MetricID, shared.MetricsResult) {
	return 0, shared.MetricsSuccess
}
func (f *fakeConfigHandle) HttpCallout(_ string, _ [][2]string, _ []byte, _ uint64, _ shared.HttpCalloutCallback) (shared.HttpCalloutInitResult, uint64) {
	return shared.HttpCalloutInitClusterNotFound, 0
}
func (f *fakeConfigHandle) StartHttpStream(_ string, _ [][2]string, _ []byte, _ bool, _ uint64, _ shared.HttpStreamCallback) (shared.HttpCalloutInitResult, uint64) {
	return shared.HttpCalloutInitClusterNotFound, 0
}
func (f *fakeConfigHandle) SendHttpStreamData(_ uint64, _ []byte, _ bool) bool  { return false }
func (f *fakeConfigHandle) SendHttpStreamTrailers(_ uint64, _ [][2]string) bool { return false }
func (f *fakeConfigHandle) ResetHttpStream(_ uint64)                            {}
func (f *fakeConfigHandle) GetScheduler() shared.Scheduler                      { return nil }

// -- Benchmarks --

// BenchmarkHeaderAuthAccept benchmarks the hot path of the header-auth filter
// when the x-api-key header is present (request accepted, forwarded upstream).
// This is the most common case.
//
// NOTE: reports 1 alloc/op on the fake (var key shared.UnsafeEnvoyBuffer escapes
// to heap because OnRequestHeaders exceeds the inliner budget and the interface
// call on headers is conservative). The real CGO path under live Envoy shows 0
// allocs/op, confirmed by the flamegraph in bench/profiles/.
func BenchmarkHeaderAuthAccept(b *testing.B) {
	factory, _ := headerauth.NewFactory(&fakeConfigHandle{}, nil)
	// BenchFilterHandle: zero-alloc Set/Add (no mutation recording noise).
	// x-user-id is pre-populated so Set is an overwrite, not a map expansion.
	fh := fake.NewBenchFilterHandle(
		fake.WithHeaders(map[string]string{
			"x-api-key":    "secret-key-abc",
			":path":        "/v1/chat/completions",
			":method":      "POST",
			"content-type": "application/json",
			"x-user-id":    "",
		}),
	)

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		filter := factory.Create(fh)
		filter.OnRequestHeaders(fh.RequestHeaders(), false)
		filter.OnStreamComplete()
	}
}

// BenchmarkHeaderAuthReject benchmarks the reject path (missing x-api-key).
// SendLocalResponse allocates; this documents the cost, not eliminates it.
func BenchmarkHeaderAuthReject(b *testing.B) {
	factory, _ := headerauth.NewFactory(&fakeConfigHandle{}, nil)
	fh := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{
			":path":   "/v1/chat/completions",
			":method": "POST",
		}),
	)

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		filter := factory.Create(fh)
		filter.OnRequestHeaders(fh.RequestHeaders(), false)
		filter.OnStreamComplete()
	}
}

// BenchmarkGetOne benchmarks HeaderMap.GetOne: the zero-alloc header read path.
func BenchmarkGetOne(b *testing.B) {
	fh := fake.NewFakeHeaderMap(map[string]string{
		"authorization": "Bearer token",
		":path":         "/v1/chat/completions",
		"content-type":  "application/json",
	})

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		_ = fh.GetOne("authorization")
	}
}

// BenchmarkGetOneInto benchmarks HeaderMap.GetOneInto: the v2 zero-allocation
// path where the caller provides the destination buffer. On the real CGO path
// this eliminates the &valueView heap escape that GetOne incurs. On the fake
// (pure Go) both are zero-alloc, but the benchmark documents the intended usage
// and keeps the alloc ceiling enforced in CI.
func BenchmarkGetOneInto(b *testing.B) {
	fh := fake.NewFakeHeaderMap(map[string]string{
		"authorization": "Bearer token",
		":path":         "/v1/chat/completions",
		"content-type":  "application/json",
	})

	var buf shared.UnsafeEnvoyBuffer
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		_ = fh.GetOneInto("authorization", &buf)
	}
}

// BenchmarkGet benchmarks HeaderMap.Get: allocates even on a hit.
// Baseline to compare against GetOne.
func BenchmarkGet(b *testing.B) {
	fh := fake.NewFakeHeaderMap(map[string]string{
		"authorization": "Bearer token",
	})

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		_ = fh.Get("authorization")
	}
}

// BenchmarkGetMiss benchmarks HeaderMap.Get on a key that does not exist.
// Documents the miss-path alloc (upstream returns []UnsafeEnvoyBuffer{}, luwes returns nil).
func BenchmarkGetMiss(b *testing.B) {
	fh := fake.NewFakeHeaderMap(map[string]string{
		"content-type": "application/json",
	})

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		_ = fh.Get("x-api-key")
	}
}

// BenchmarkGetAll benchmarks HeaderMap.GetAll with 10 headers.
// Documents the double-alloc per call.
func BenchmarkGetAll(b *testing.B) {
	fh := fake.NewFakeHeaderMap(map[string]string{
		"authorization":   "Bearer token",
		":path":           "/v1/chat/completions",
		":method":         "POST",
		"content-type":    "application/json",
		"content-length":  "128",
		"accept":          "application/json",
		"user-agent":      "test/1.0",
		"x-request-id":    "abc-123",
		"x-forwarded-for": "1.2.3.4",
		"x-api-key":       "secret",
	})

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		_ = fh.GetAll()
	}
}

// BenchmarkGetChunks benchmarks BodyBuffer.GetChunks with a 1KB body.
// Fake returns a single chunk; documents baseline per-call cost.
func BenchmarkGetChunks(b *testing.B) {
	body := make([]byte, 1024)
	fb := fake.NewFakeBodyBuffer(body)

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		_ = fb.GetChunks()
	}
}

// BenchmarkFilterCreate benchmarks factory.Create: the per-request filter allocation.
// This is the highest-priority optimization target (handle pool).
func BenchmarkFilterCreate(b *testing.B) {
	factory, _ := headerauth.NewFactory(&fakeConfigHandle{}, nil)
	fh := fake.NewFilterHandle()

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		_ = factory.Create(fh)
	}
}
