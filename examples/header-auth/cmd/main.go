// Package main builds the header-auth dynamic module with a pprof admin server.
// The pprof server starts when Envoy loads the filter config (not at init time).
// It listens on 127.0.0.1:6061 by default; override with LUWES_PPROF_ADDR.
//
// Capture a heap profile during load:
//
//	go tool pprof -alloc_objects http://127.0.0.1:6061/debug/pprof/allocs
package main

import (
	"encoding/json"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"sync"

	sdk "github.com/dio/luwes"
	_ "github.com/dio/luwes/abi_impl"
	headerauth "github.com/dio/luwes/examples/header-auth"
	"github.com/dio/luwes/shared"
)

func init() {
	sdk.RegisterHttpFilterConfigFactories(map[string]shared.HttpFilterConfigFactory{
		"header-auth": &configFactory{},
	})
}

type configFactory struct {
	once sync.Once
	ln   net.Listener
	srv  *http.Server
}

func (f *configFactory) Create(h shared.HttpFilterConfigHandle, raw []byte) (shared.HttpFilterFactory, error) {
	// Start pprof server once, on first config load.
	// At this point the Go runtime is fully up inside the .so.
	f.once.Do(func() {
		addr := "127.0.0.1:6061"
		if a := os.Getenv("LUWES_PPROF_ADDR"); a != "" {
			addr = a
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			h.Log(shared.LogLevelWarn, "luwes pprof: bind failed on %s: %v", addr, err)
			return
		}
		f.ln = ln
		mux := http.NewServeMux()
		mux.HandleFunc("/debug/pprof/", http.DefaultServeMux.ServeHTTP)
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		})
		f.srv = &http.Server{Handler: mux}
		go f.srv.Serve(ln)
		h.Log(shared.LogLevelInfo, "luwes pprof: http://%s/debug/pprof/", ln.Addr())
	})
	return headerauth.NewFactory(h, raw)
}

func (f *configFactory) CreatePerRoute(_ []byte) (any, error) { return nil, nil }

func main() {}
