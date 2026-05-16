// Package fake provides in-process test doubles for the luwes SDK interfaces.
//
// FakeHeaderMap and FakeBodyBuffer implement the shared.HeaderMap and
// shared.BodyBuffer interfaces for use in unit tests and benchmarks without
// a real Envoy process or CGO.
//
// FakeFilterHandle implements shared.HttpFilterHandle. It is the primary
// test double for benchmarking filter code. Pass it to a factory's Create()
// method to exercise filter logic in pure Go:
//
//	fh := fake.NewFilterHandle(
//	    fake.WithHeaders(map[string]string{
//	        "authorization": "Bearer token",
//	        ":path":         "/v1/chat/completions",
//	    }),
//	)
//	filter := factory.Create(fh)
//	filter.OnRequestHeaders(fh.RequestHeaders(), false)
//	filter.OnStreamComplete()
package fake
