// Package sahl (سهل) is an ergonomic HTTP filter API for Envoy dynamic modules
// built on top of luwes.
//
// sahl provides familiar Go types ([Request], [Writer], [Header]) without
// the goroutine-per-request overhead of jisr. Handlers run on the Envoy worker
// thread by default. Blocking work is opt-in via [Writer.Go].
//
// # Registration
//
// The simplest filter (synchronous, runs on the worker thread):
//
//	func init() {
//	    sahl.Register("my-filter", func(w *sahl.Writer, r *sahl.Request) {
//	        key, ok := r.Header.Peek("x-api-key")
//	        if !ok {
//	            w.Send(http.StatusUnauthorized, `{"error":"missing key"}`)
//	            return
//	        }
//	        w.SetRequestHeader("x-user-id", key)
//	    })
//	}
//
// For filters that need per-config state (parsed config, metric IDs):
//
//	func init() {
//	    sahl.RegisterFactory("my-filter",
//	        func(h sahl.ConfigHandle) (sahl.HandlerFunc, error) {
//	            counter, err := h.DefineCounter("my_requests_total", "result")
//	            if err != nil { return nil, err }
//	            cfg, err := parseConfig(h.RawConfig())
//	            if err != nil { return nil, err }
//	            return func(w *sahl.Writer, r *sahl.Request) {
//	                // counter and cfg are per-factory, not per-request
//	                w.IncrementCounter(counter, 1, "ok")
//	            }, nil
//	        },
//	    )
//	}
//
// # Multiple filters in one .so
//
// A single Go package can register several filters by calling Register
// (or any variant) multiple times in init(). Each filter is keyed by its
// name string, which must match the filter_name field in Envoy's YAML config.
//
//	func init() {
//	    sahl.Register("spa", SPAHandler)
//	    sahl.Register("api-backend", sahl.Chain(APIHandler, apiLogMiddleware))
//	}
//
// Each call creates an independent entry in sahl's global registry. The two
// filters are completely isolated: separate handler functions, separate
// per-request pools (filterPool, requestPool, writerPool), separate metric
// namespaces. They share the same process and the same embedded assets
// (e.g. //go:embed ui/dist) but nothing else.
//
// Envoy wires them independently via filter_name in envoy.yaml:
//
//	http_filters:
//	  - name: api-backend          # routes to APIHandler
//	    typed_config:
//	      "@type": ...DynamicModuleFilter
//	      dynamic_module_config: { name: spa }
//	      filter_name: api-backend  # must match the Register() call
//	  - name: spa                  # routes to SPAHandler
//	    typed_config:
//	      "@type": ...DynamicModuleFilter
//	      dynamic_module_config: { name: spa }
//	      filter_name: spa         # must match the Register() call
//
// Both filters are loaded from the same dynamic_module_config.name (.so file),
// but dispatched to different Go handlers based on filter_name. Each filter
// in the chain runs independently: api-backend first (handles /api/* and
// passes through everything else), spa second (serves assets and index.html).
//
// Registering the same name twice panics at startup with "BUG: sahl filter
// %q registered twice". This catches copy-paste errors before Envoy loads.
//
// # Blocking work
//
// When a handler needs to make an external call (Redis, auth service, DB),
// call [Writer.Go] to upgrade the request to goroutine mode:
//
//	func myHandler(w *sahl.Writer, r *sahl.Request) {
//	    // Fast path: header-only, runs on worker thread, 0 goroutines.
//	    key, ok := r.Header.Peek("x-api-key")
//	    if !ok {
//	        w.Send(http.StatusUnauthorized, `{"error":"missing key"}`)
//	        return
//	    }
//
//	    // Slow path: block on external call.
//	    w.Go(func(ctx context.Context) {
//	        session, err := redis.Get(ctx, key)
//	        if err != nil {
//	            w.Send(http.StatusUnauthorized, `{"error":"invalid key"}`)
//	            return
//	        }
//	        w.SetRequestHeader("x-user-id", session)
//	    })
//	    // Handler returns here. Worker thread is released.
//	    // Mutations are applied when the goroutine finishes.
//	}
//
// # Building a .so
//
// Wire via cmd/main.go, identical to raw luwes:
//
//	package main
//
//	import (
//	    sdk  "github.com/dio/luwes"
//	    _    "github.com/dio/luwes/abi_impl"
//	    _    "your/filter/package"
//	)
//
//	func init() { sdk.RegisterHttpFilterConfigFactories(sahl.Factories()) }
//	func main() {}
//
// # Alloc budget
//
// Warm pool, synchronous handler reading 2 headers:
//
//	Method + Path + Host:  3 allocs (fixed, pre-copied at callback entry)
//	Header.Get per key:    1 alloc per unique key read
//	Header.Peek per key:   0 allocs (unsafe string, valid within callback only)
//
// vs jisr: goroutine + all headers copied = 20+ allocs per request.
// vs raw luwes: 0 allocs (GetOneInto), no ergonomics.
package sahl
