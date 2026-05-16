// Package luwes provides filter registration and composition for Envoy dynamic modules.
//
// The root package is the composition layer. Filter authors call Register in their
// init() functions, then wire everything from cmd/main.go with a single call to
// sdk.RegisterHttpFilterConfigFactories(luwes.Factories()).
//
// # Registration
//
// The simplest registration -- provide a factory function that returns a
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
// For raw control -- when you already have a shared.HttpFilterConfigFactory:
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
	_ "net/http/pprof"
	"os"
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
// after Register calls -- at that point the Go runtime is fully initialized
// inside the .so and net.Listen is safe to call.
//
// addr is the TCP address to bind. Empty string defaults to 127.0.0.1:6061.
// Set LUWES_PPROF_ADDR to override at runtime without rebuilding.
//
// The server exposes:
//
//	GET /debug/pprof/  -- pprof index
//	GET /debug/pprof/allocs -- allocation profile (primary flamegraph target)
//	GET /debug/pprof/heap   -- heap snapshot
//	GET /healthz            -- {"status":"ok"}
//
// StartPprof is a no-op if called more than once or if the port is already in use.
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
			// Port in use or permission denied -- skip silently.
			// This is intentional: StartPprof is a best-effort helper.
			return
		}
		mux := http.NewServeMux()
		// pprof handlers registered by the _ "net/http/pprof" import
		// into http.DefaultServeMux. Forward them here.
		mux.HandleFunc("/debug/pprof/", http.DefaultServeMux.ServeHTTP)
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"}) //nolint:errcheck
		})
		srv := &http.Server{Handler: mux}
		go srv.Serve(ln) //nolint:errcheck
		fmt.Fprintf(os.Stderr, "[luwes] pprof on http://%s/debug/pprof/\n", ln.Addr())
	})
}
