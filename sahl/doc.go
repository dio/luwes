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
// # Factories
//
// ## The three lifetimes
//
// Envoy's dynamic module API has three distinct creation points:
//
//  1. program init          -- once per process (global registry)
//  2. filter config create  -- once per Envoy listener / filter chain
//  3. filter instance create -- once per HTTP request
//
// sahl has a type for each:
//
//	configFactory   -- created once per registered name; Envoy calls its
//	                   Create method once per filter_chain that references
//	                   this filter. Produces a filterFactory.
//	filterFactory   -- one per listener. Holds the resolved HandlerFunc for
//	                   that listener. Produces sahlFilters.
//	sahlFilter      -- one per request. Pool-allocated. Zero-alloced on warm
//	                   pool hit.
//
// ## Registration function comparison
//
// Quick reference -- pick the row that matches your filter:
//
//	Registration function          Per-listener  Metrics  Body  Response  Multi-listener safe
//	------------------------------ ------------- -------- ----- --------- -------------------
//	Register                       no            no       no    no        yes (stateless)
//	RegisterWithConfig             no            yes      no    no        NO (package vars overwritten)
//	RegisterWithResponse           no            no       no    yes       yes (stateless)
//	RegisterWithConfigAndResponse  no            yes      no    yes       NO (package vars overwritten)
//	RegisterWithBody               no            no       yes   no        yes (stateless)
//	RegisterWithBodyAndResponse    no            no       yes   yes       yes (stateless)
//	RegisterWithBodyConfigAndResponse no          yes     yes   yes       NO (package vars overwritten)
//	RegisterFactory                YES           YES      no    no        YES (closure per listener)
//
// "Multi-listener safe" means two or more envoy.yaml listeners can use this
// filter with different filter_config bytes without one overwriting the other.
// Any function that writes to package-level vars in its configFn is NOT safe
// for multi-listener use.
//
// ## Side-by-side: RegisterWithConfig vs RegisterFactory
//
// Both patterns define a counter and parse config. The difference is where
// that state lives and what happens when Envoy creates a second listener.
//
// RegisterWithConfig -- state in package vars, second listener overwrites first:
//
//	// package-level: shared by ALL listeners using this filter
//	var (
//	    reqTotal sahl.MetricID
//	    allowed  map[string]struct{}
//	)
//
//	func init() {
//	    sahl.RegisterWithConfig("auth",
//	        func(h sahl.ConfigHandle) error {
//	            cfg := parseConfig(h.RawConfig()) // listener 2 overwrites listener 1
//	            allowed = buildSet(cfg.AllowedKeys)
//	            reqTotal, _ = h.DefineCounter("auth_requests_total", "result")
//	            return nil
//	        },
//	        func(w *sahl.Writer, r *sahl.Request) {
//	            key, _ := r.Header.Peek("x-api-key")
//	            if _, ok := allowed[key]; !ok { // reads the LAST-written allowed
//	                w.IncrementCounter(reqTotal, 1, "rejected")
//	                w.Send(401, `{"error":"unauthorized"}`)
//	                return
//	            }
//	            w.IncrementCounter(reqTotal, 1, "allowed")
//	        },
//	    )
//	}
//
// RegisterFactory -- state in closure, each listener gets its own copy:
//
//	func init() {
//	    sahl.RegisterFactory("auth",
//	        func(h sahl.ConfigHandle) (sahl.HandlerFunc, error) {
//	            // runs once per listener; vars are local to this call
//	            cfg := parseConfig(h.RawConfig())
//	            allowed := buildSet(cfg.AllowedKeys) // listener 1 and 2 each get their own
//	            reqTotal, err := h.DefineCounter("auth_requests_total", "result")
//	            if err != nil {
//	                return nil, err
//	            }
//	            return func(w *sahl.Writer, r *sahl.Request) {
//	                key, _ := r.Header.Peek("x-api-key")
//	                if _, ok := allowed[key]; !ok { // reads THIS listener's allowed
//	                    w.IncrementCounter(reqTotal, 1, "rejected")
//	                    w.Send(401, `{"error":"unauthorized"}`)
//	                    return
//	                }
//	                w.IncrementCounter(reqTotal, 1, "allowed")
//	            }, nil
//	        },
//	    )
//	}
//
// ## When to use which registration function
//
// Register: handler needs no config, no metrics, no per-listener state. A
// passthrough tagger, an always-reject guard, a debug header injector.
//
//	sahl.Register("my-filter", myHandlerFunc)
//
// RegisterWithConfig: one listener uses this filter and the handler needs
// metrics or parsed config. The configFn runs once, writes to package-level
// vars, the handler reads them. Correct as long as the single-listener
// constraint holds. The failure mode is silent: if a second listener in
// envoy.yaml uses the same filter_name with different filter_config bytes,
// the configFn runs a second time and overwrites the package vars. First
// listener now uses the second listener's config. No error, no warning.
//
//	sahl.RegisterWithConfig("my-filter", configFn, handlerFn)
//
// RegisterFactory: multiple listeners may use this filter with different
// configs, or you want to future-proof. The factory is called once per
// filter_chain. Each call returns a new HandlerFunc that closes over its
// own parsed config and metric IDs. Two listeners, two closures, complete
// isolation. This is the correct default once you have any per-listener
// state at all.
//
//	sahl.RegisterFactory("my-filter", func(h sahl.ConfigHandle) (sahl.HandlerFunc, error) {
//	    cfg, err := parseConfig(h.RawConfig())
//	    if err != nil { return nil, err }
//	    counter, err := h.DefineCounter("my_requests_total", "result")
//	    if err != nil { return nil, err }
//	    allowed := make(map[string]struct{}, len(cfg.AllowedKeys))
//	    for _, k := range cfg.AllowedKeys { allowed[k] = struct{}{} }
//	    return func(w *sahl.Writer, r *sahl.Request) {
//	        key, _ := r.Header.Peek("x-api-key")
//	        if _, ok := allowed[key]; !ok {
//	            w.IncrementCounter(counter, 1, "rejected")
//	            w.Send(401, `{"error":"unauthorized"}`)
//	            return
//	        }
//	        w.IncrementCounter(counter, 1, "allowed")
//	    }, nil
//	})
//
// See examples/sahl/auth for a complete per-listener isolation example.
//
// ## The metric ID constraint
//
// Metric IDs must be defined at filter config time, not per-request.
// ConfigHandle is only available in configFn or the factory function --
// you cannot call DefineCounter from a HandlerFunc. Envoy allocates the
// metric slot once; calling DefineCounter per-request would re-define it
// on every request (undefined behavior, likely panic or silent leak).
//
// With RegisterWithConfig, metric IDs live in package vars -- correct for
// single-listener deployments. With RegisterFactory, they are captured in
// the closure -- each listener gets its own MetricID value and its own
// Envoy stat slot.
//
// ## The configHandleImpl wrapping subtlety
//
// configFactory.Create wraps the incoming shared.HttpFilterConfigHandle
// and the raw []byte (the filter_config bytes from envoy.yaml) into a
// configHandleImpl. ConfigHandle.RawConfig() returns c.raw (the []byte
// parameter), not the underlying handle's RawConfig(). This matters in
// tests: you must pass the raw config bytes as the second arg to Create,
// not only set them on the fake handle:
//
//	factory, err := def.Create(&fakeHandle{}, []byte(configJSON))  // correct
//	factory, err := def.Create(&fakeHandle{raw: configJSON}, nil)  // wrong: RawConfig() returns nil
//
// ## The filterDef copy in Create
//
// configFactory.Create copies the registered filterDef into a new struct
// before calling the factory or configFn. This isolates each filterFactory's
// handler from the global registry entry. Without the copy, a second Create
// call (second listener) would race to mutate the same filterDef. For
// RegisterFactory the copy's handler field is always overwritten by the
// factory result; the copy is defensive bookkeeping.
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
