package e2e

// callout_mock_test.go: mock HTTP server for callout/stream/do e2e tests.
//
// Routes:
//   GET  /auth-ok     200 + x-auth-user: testuser
//   GET  /auth-deny   401 + no user
//   POST /stream-chunks  200 + 3 SSE-style chunks (tests HTTPStream data events)
//   POST /stream-reset   connection close mid-stream (triggers reset)

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

var (
	mockCalloutServer  *http.Server
	calloutUpstreamPort int
)

func startMockCalloutServer() int {
	mux := http.NewServeMux()

	// Auth routes used by callout-sahl and do-sahl.
	mux.HandleFunc("/auth-ok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-auth-user", "testuser")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"ok":true}`)
	})

	// /ok is used by mutable-body-sahl e2e test: returns 200 with a plain body.
	// ResponseFlags will be empty (real upstream response, no infrastructure failure).
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"original":true}`)
	})

	mux.HandleFunc("/auth-deny", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"denied"}`)
	})

	// Stream routes used by stream-sahl.
	mux.HandleFunc("/stream-chunks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, `{"chunk":%d}`, i)
			flusher.Flush()
			time.Sleep(5 * time.Millisecond)
		}
	})

	mux.HandleFunc("/stream-reset", func(w http.ResponseWriter, r *http.Request) {
		// Hijack and close immediately to trigger a reset on the client side.
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack unsupported", http.StatusInternalServerError)
			return
		}
		conn, _, _ := hj.Hijack()
		conn.Close()
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: mock callout server listen failed: %v\n", err)
		os.Exit(1)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	mockCalloutServer = &http.Server{Handler: mux}
	go mockCalloutServer.Serve(ln) //nolint:errcheck
	return port
}
