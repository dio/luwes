// Package luwes provides filter registration and composition for Envoy dynamic modules.
//
// The root package is the composition layer. Filter authors call Register in their
// init() functions, then wire everything from cmd/main.go with a single call to
// sdk.RegisterHttpFilterConfigFactories(luwes.Factories()).
//
// # Registration
//
// The simplest registration: provide a factory function that returns a
// shared.HttpFilterFactory from a config handle and raw config bytes:
//
//	func init() {
//	    luwes.Register("my-filter", myfilter.NewFactory)
//	}
//
// For filters that need no config parsing, use the zero-config variant:
//
//	func init() {
//	    luwes.RegisterSimple("my-filter", func() shared.HttpFilterFactory {
//	        return &myFilterFactory{}
//	    })
//	}
//
// For raw control (when you already have a shared.HttpFilterConfigFactory):
//
//	func init() {
//	    luwes.RegisterRaw("my-filter", &myConfigFactory{})
//	}
//
// # Wiring
//
// From your cmd/main.go init():
//
//	import (
//	    sdk  "github.com/dio/luwes"
//	    _    "github.com/dio/luwes/abi_impl"
//	    _    "your/filter/package" // triggers its init() which calls luwes.Register
//	)
//
//	func init() {
//	    sdk.RegisterHttpFilterConfigFactories(luwes.Factories())
//	}
//
//	func main() {}
//
// # Pprof
//
// Optionally start a Go pprof admin server. Call from your cmd/main.go init()
// so it starts when Envoy loads the filter config (not during dlopen):
//
//	func init() {
//	    luwes.Register("my-filter", myfilter.NewFactory)
//	    luwes.StartPprof("") // default: 127.0.0.1:6061; set LUWES_PPROF_ADDR to override
//	}
//
// Capture a heap profile during load:
//
//	go tool pprof -alloc_objects http://127.0.0.1:6061/debug/pprof/allocs
package sdk

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime/debug"
	"sync"

	"github.com/dio/luwes/shared"
)

// FactoryFunc is the standard factory signature. Matches the pattern used by
// NewFactory functions in luwes filter packages.
type FactoryFunc func(shared.HttpFilterConfigHandle, []byte) (shared.HttpFilterFactory, error)

// registry holds all filters registered via Register, RegisterSimple, and RegisterRaw.
// Protected by registryMu. Written only from init(), read only after init() completes.
var (
	registry   = make(map[string]shared.HttpFilterConfigFactory)
	registryMu sync.Mutex
)

// Register registers a filter by name using a FactoryFunc.
// fn is called once per Envoy filter config load to produce a shared.HttpFilterFactory.
// Call from your filter package's init().
func Register(name string, fn FactoryFunc) {
	RegisterRaw(name, &factoryFuncAdapter{fn: fn})
}

// RegisterSimple registers a filter that needs no config parsing.
// fn is called once per Envoy filter config load.
func RegisterSimple(name string, fn func() shared.HttpFilterFactory) {
	RegisterRaw(name, &factoryFuncAdapter{
		fn: func(_ shared.HttpFilterConfigHandle, _ []byte) (shared.HttpFilterFactory, error) {
			return fn(), nil
		},
	})
}

// RegisterRaw registers a filter with a pre-built shared.HttpFilterConfigFactory.
// Use when you need full control over the config factory lifecycle.
func RegisterRaw(name string, factory shared.HttpFilterConfigFactory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("BUG: luwes filter %q registered twice", name))
	}
	registry[name] = factory
}

// Factories returns all registered filter config factories as a map suitable
// for sdk.RegisterHttpFilterConfigFactories.
//
// Call once from your cmd/main.go init():
//
//	sdk.RegisterHttpFilterConfigFactories(luwes.Factories())
func Factories() map[string]shared.HttpFilterConfigFactory {
	registryMu.Lock()
	defer registryMu.Unlock()
	out := make(map[string]shared.HttpFilterConfigFactory, len(registry))
	for k, v := range registry {
		out[k] = v
	}
	return out
}

// factoryFuncAdapter wraps a FactoryFunc as a shared.HttpFilterConfigFactory.
type factoryFuncAdapter struct {
	fn FactoryFunc
}

func (a *factoryFuncAdapter) Create(h shared.HttpFilterConfigHandle, raw []byte) (shared.HttpFilterFactory, error) {
	return a.fn(h, raw)
}

func (a *factoryFuncAdapter) CreatePerRoute(_ []byte) (any, error) { return nil, nil }

// -- Pprof admin server --

var pprofOnce sync.Once

// StartPprof starts a Go pprof admin server. Call from your cmd/main.go init()
// after Register calls; at that point the Go runtime is fully initialized
// inside the .so and net.Listen is safe to call.
//
// addr is the TCP address to bind. Empty string defaults to 127.0.0.1:6061.
// Set LUWES_PPROF_ADDR to override at runtime without rebuilding.
//
// The server exposes:
//
//	GET /debug/pprof/           pprof index
//	GET /debug/pprof/allocs     allocation profile (primary flamegraph target)
//	GET /debug/pprof/heap       heap snapshot
//	GET /debug/pprof/goroutine  goroutine stacks
//	GET /debug/pprof/block      block profile
//	GET /debug/pprof/mutex      mutex profile
//	GET /debug/pprof/profile    CPU profile (?seconds=N)
//	GET /debug/pprof/trace      execution trace
//	GET /healthz                {"status":"ok"}
//	GET /readyz                 {"status":"ok"}
//	GET /version                {"module":"...","version":"..."}
//
// StartPprof is a no-op if called more than once.
// Bind errors are logged to stderr instead of swallowed.
func StartPprof(addr string) {
	pprofOnce.Do(func() {
		if a := os.Getenv("LUWES_PPROF_ADDR"); a != "" {
			addr = a
		}
		if addr == "" {
			addr = "127.0.0.1:6061"
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[luwes] pprof bind %s: %v\n", addr, err)
			return
		}
		mux := http.NewServeMux()

		// pprof: explicit handlers, no DefaultServeMux coupling.
		mux.HandleFunc("GET /debug/pprof/", pprof.Index)
		mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
		for _, name := range []string{"goroutine", "heap", "allocs", "block", "mutex", "threadcreate"} {
			mux.Handle("GET /debug/pprof/"+name, pprof.Handler(name))
		}

		health := func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"}) //nolint:errcheck
		}
		mux.HandleFunc("GET /healthz", health)
		mux.HandleFunc("GET /readyz", health)

		mux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			info, ok := debug.ReadBuildInfo()
			if !ok {
				json.NewEncoder(w).Encode(map[string]string{"module": "unknown", "version": "unknown"}) //nolint:errcheck
				return
			}
			json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
				"module":  info.Main.Path,
				"version": info.Main.Version,
			})
		})

		srv := &http.Server{Handler: mux}
		go srv.Serve(ln) //nolint:errcheck
		fmt.Fprintf(os.Stderr, "[luwes] pprof on http://%s/debug/pprof/\n", ln.Addr())
	})
}

// AccessLoggerFactoryFunc is the factory constructor for access loggers.
// Called once per access logger config load on the main thread.
type AccessLoggerFactoryFunc func(
	shared.AccessLoggerConfigHandle, []byte,
) (shared.AccessLoggerFactory, error)

var (
	accessLoggerRegistry   = make(map[string]shared.AccessLoggerConfigFactory)
	accessLoggerRegistryMu sync.Mutex
)

// RegisterAccessLogger registers an access logger by name using a factory func.
// Call from your access logger package's init().
//
//	func init() {
//	    luwes.RegisterAccessLogger("my-logger", mylogger.NewFactory)
//	}
//
// Then wire from cmd/main.go:
//
//	sdk.RegisterAccessLoggerConfigFactories(luwes.AccessLoggerFactories())
func RegisterAccessLogger(name string, fn AccessLoggerFactoryFunc) {
	accessLoggerRegistryMu.Lock()
	defer accessLoggerRegistryMu.Unlock()
	if _, ok := accessLoggerRegistry[name]; ok {
		panic("access logger already registered: " + name)
	}
	accessLoggerRegistry[name] = &accessLoggerFactoryFuncAdapter{fn: fn}
}

// AccessLoggerFactories returns all registered access logger config factories.
func AccessLoggerFactories() map[string]shared.AccessLoggerConfigFactory {
	accessLoggerRegistryMu.Lock()
	defer accessLoggerRegistryMu.Unlock()
	out := make(map[string]shared.AccessLoggerConfigFactory, len(accessLoggerRegistry))
	for k, v := range accessLoggerRegistry {
		out[k] = v
	}
	return out
}

type accessLoggerFactoryFuncAdapter struct {
	fn AccessLoggerFactoryFunc
}

func (a *accessLoggerFactoryFuncAdapter) Create(
	h shared.AccessLoggerConfigHandle, raw []byte,
) (shared.AccessLoggerFactory, error) {
	return a.fn(h, raw)
}
