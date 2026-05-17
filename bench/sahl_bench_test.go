package bench

// BenchmarkSahl* documents the per-request allocation cost of the sahl ergonomic
// layer on top of raw luwes. All benchmarks use the fake path (pure Go, no CGO).
//
// Fake vs CGO discrepancy:
//   - Pool reuse (filterPool, requestPool, writerPool): both paths land at 0
//     allocs for the pool get itself after warmup.
//   - Request.reset() calls ToString() 3 times (method, path, host): each copies
//     from Envoy memory into a Go string. On CGO: 3 unavoidable allocs.
//     On fake: same 3 allocs (strings.Clone forces the copy).
//   - Header.Peek: 0 allocs on both paths (unsafe.String, no copy).
//   - Header.Get (first call): 1 alloc (ToString + cache insert). Repeat: 0.
//   - Writer.flush with reqMuts: 0 allocs (mutations are pre-queued).

import (
	"net/http"
	"testing"

	"github.com/dio/luwes/sahl"
	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
)

// warmupSahlPools pre-warms all three sahl internal pools (filter, request, writer)
// so benchmark iterations measure steady-state cost, not pool-miss allocation.
func warmupSahlPools(factory shared.HttpFilterFactory, fh shared.HttpFilterHandle) {
	const warmup = 16
	filters := make([]shared.HttpFilter, warmup)
	for i := range filters {
		filters[i] = factory.Create(fh)
		filters[i].OnRequestHeaders(fh.RequestHeaders(), false)
	}
	for _, f := range filters {
		f.OnStreamComplete()
		f.OnDestroy()
	}
}

// buildSahlFactory builds a sahl filter factory for the given handler.
// Uses fakeConfigHandle defined in bench_test.go (same package).
func buildSahlFactory(h sahl.HandlerFunc) shared.HttpFilterFactory {
	cfgFactory := sahl.Factory(h)
	factory, err := cfgFactory.Create(&fakeConfigHandle{}, nil)
	if err != nil {
		panic("sahl: Factory.Create failed: " + err.Error())
	}
	return factory
}

// BenchmarkSahlHandlerAccept benchmarks the full sahl steady-state accept path:
// pool get (filter+request+writer) -> Header.Peek -> SetRequestHeader -> flush -> pool put.
//
// Allocation breakdown per request:
//
//	Fake (pure Go, this bench):
//	  reset:  1 alloc for var buf UnsafeEnvoyBuffer (escapes via interface dispatch
//	          to FakeHeaderMap.GetOneInto; one buf shared for all three fields)
//	  Method: 1 alloc (strings.Clone into Go memory)
//	  Path:   1 alloc (strings.Clone into Go memory)
//	  Host:   1 alloc (strings.Clone into Go memory)
//	  Peek:   1 alloc for var buf UnsafeEnvoyBuffer (same interface escape)
//	  Total:  5 allocs/op
//
//	Real CGO path (live Envoy):
//	  reset:  0 allocs for buf (not dispatched through interface; stack-allocated)
//	  Method: 1 alloc (strings.Clone, unavoidable copy into Go memory)
//	  Path:   1 alloc (strings.Clone)
//	  Host:   1 alloc (strings.Clone)
//	  Peek:   0 allocs (buf inlined, unsafe.String is zero-copy)
//	  Total:  3 allocs/op (the 3 pre-copies; cannot eliminate without lazy reads)
//
// Compare to raw header-auth (BenchmarkHeaderAuthAccept): 0 allocs on CGO path.
// The 3 extra allocs are the cost of sahl's Method/Path/Host pre-copy convenience.
// Use Peek (not Get) on the hot path to avoid the cache-miss alloc.
func BenchmarkSahlHandlerAccept(b *testing.B) {
	handler := func(w *sahl.Writer, r *sahl.Request) {
		key, ok := r.Header.Peek("x-api-key")
		if !ok || len(key) == 0 {
			w.Send(http.StatusUnauthorized, `{"error":"missing x-api-key"}`)
			return
		}
		w.SetRequestHeader("x-user-id", key)
	}

	fh := fake.NewBenchFilterHandle(
		fake.WithHeaders(map[string]string{
			"x-api-key":    "secret-key-abc",
			":path":        "/v1/chat/completions",
			":method":      "POST",
			":authority":   "api.example.com",
			"content-type": "application/json",
			"x-user-id":    "",
		}),
	)

	factory := buildSahlFactory(handler)
	warmupSahlPools(factory, fh)

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		f := factory.Create(fh)
		f.OnRequestHeaders(fh.RequestHeaders(), false)
		f.OnStreamComplete()
		f.OnDestroy()
	}
}

// BenchmarkSahlHandlerReject benchmarks the reject path:
// Peek miss -> w.Send -> SendLocalResponse. The body conversion inside
// SendLocalResponse is the primary cost beyond the 3 ToString copies.
func BenchmarkSahlHandlerReject(b *testing.B) {
	handler := func(w *sahl.Writer, r *sahl.Request) {
		key, ok := r.Header.Peek("x-api-key")
		if !ok || len(key) == 0 {
			w.Send(http.StatusUnauthorized, `{"error":"missing x-api-key"}`)
			return
		}
		w.SetRequestHeader("x-user-id", key)
	}

	fh := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{
			":path":      "/v1/chat/completions",
			":method":    "POST",
			":authority": "api.example.com",
		}),
	)

	factory := buildSahlFactory(handler)
	warmupSahlPools(factory, fh)

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		f := factory.Create(fh)
		f.OnRequestHeaders(fh.RequestHeaders(), false)
		f.OnStreamComplete()
		f.OnDestroy()
	}
}

// BenchmarkSahlHeaderPeek benchmarks Header.Peek contribution within a
// full request cycle. Since Peek is 0 allocs on CGO (GetOneInto + unsafe.String),
// any alloc here is the fake-path var buf escape shared with reset.
// Compare BenchmarkSahlHandlerAccept (5 allocs) with a no-op handler (4 allocs)
// to isolate Peek's 1 fake-path alloc. On CGO: 0 additional allocs.
func BenchmarkSahlHeaderPeek(b *testing.B) {
	handler := func(_ *sahl.Writer, r *sahl.Request) {
		_, _ = r.Header.Peek("x-api-key")
	}

	fh := fake.NewBenchFilterHandle(
		fake.WithHeaders(map[string]string{
			"x-api-key":  "token",
			":method":    "POST",
			":path":      "/api",
			":authority": "host",
		}),
	)

	factory := buildSahlFactory(handler)
	warmupSahlPools(factory, fh)

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		f := factory.Create(fh)
		f.OnRequestHeaders(fh.RequestHeaders(), false)
		f.OnStreamComplete()
		f.OnDestroy()
	}
}

// BenchmarkSahlHandlerNoOp benchmarks the baseline sahl per-request cost with
// a no-op handler: pool get/put + reset (method/path/host copies) only.
// 4 allocs on fake: var buf escape in reset + 3 ToString copies.
// 3 allocs on CGO: 3 ToString copies only (no buf escape without interface dispatch).
func BenchmarkSahlHandlerNoOp(b *testing.B) {
	fh := fake.NewBenchFilterHandle(
		fake.WithHeaders(map[string]string{
			":method":    "POST",
			":path":      "/api",
			":authority": "host",
		}),
	)

	factory := buildSahlFactory(func(*sahl.Writer, *sahl.Request) {})
	warmupSahlPools(factory, fh)

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		f := factory.Create(fh)
		f.OnRequestHeaders(fh.RequestHeaders(), false)
		f.OnStreamComplete()
		f.OnDestroy()
	}
}

// BenchmarkSahlHeaderGet benchmarks Header.Get: the first call per request.
// Allocates 2 beyond the NoOp baseline: var buf escape (interface dispatch)
// and ToString into cache. Repeat Get calls for the same key: 0 additional allocs.
// Total per request on fake: 6 allocs (4 NoOp + 1 buf + 1 ToString).
// On CGO: 4 allocs (3 pre-copies + 1 ToString; no buf escape).
func BenchmarkSahlHeaderGet(b *testing.B) {
	handler := func(_ *sahl.Writer, r *sahl.Request) {
		_ = r.Header.Get("x-api-key")
	}

	fh := fake.NewBenchFilterHandle(
		fake.WithHeaders(map[string]string{
			"x-api-key":  "token",
			":method":    "POST",
			":path":      "/api",
			":authority": "host",
		}),
	)

	factory := buildSahlFactory(handler)
	warmupSahlPools(factory, fh)

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		f := factory.Create(fh)
		f.OnRequestHeaders(fh.RequestHeaders(), false)
		f.OnStreamComplete()
		f.OnDestroy()
	}
}

// BenchmarkSahlHeaderGetCached benchmarks the second Header.Get call for the
// same key (cache hit): 0 additional allocs vs Peek. Total per request stays
// at 6 (the first Get within the same handler call loads the cache; the second
// is free). On CGO path: 4 allocs total (3 pre-copies + 1 first-Get ToString).
func BenchmarkSahlHeaderGetCached(b *testing.B) {
	handler := func(_ *sahl.Writer, r *sahl.Request) {
		_ = r.Header.Get("x-api-key") // populates cache: 2 allocs above NoOp
		_ = r.Header.Get("x-api-key") // cache hit: 0 additional allocs
	}

	fh := fake.NewBenchFilterHandle(
		fake.WithHeaders(map[string]string{
			"x-api-key":  "token",
			":method":    "POST",
			":path":      "/api",
			":authority": "host",
		}),
	)

	factory := buildSahlFactory(handler)
	warmupSahlPools(factory, fh)

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		f := factory.Create(fh)
		f.OnRequestHeaders(fh.RequestHeaders(), false)
		f.OnStreamComplete()
		f.OnDestroy()
	}
}
