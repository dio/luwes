// testserver is a minimal HTTP backend for testing the request-logger filter.
// Serves several routes that exercise different response conditions.
//
// Run:
//
//	go run ./examples/request-logger/testserver
//
// Routes:
//
//	GET  /ok            200 {"ok":true}
//	GET  /slow          200 after 1.5s (exercises slow request logging)
//	GET  /error         500 {"error":"internal"}
//	GET  /notfound      404
//	POST /v1/chat/completions  200, echoes body length
//	POST /v1/messages         200, echoes body length
//	GET  /health        200 {"status":"ok"}
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

func main() {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /ok", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"ok": true}) //nolint:errcheck
	})

	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "note": "slow"}) //nolint:errcheck
	})

	mux.HandleFunc("GET /error", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "internal"}) //nolint:errcheck
	})

	mux.HandleFunc("GET /notfound", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	chat := func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
		model, _ := body["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"id":    "resp-001",
			"model": model,
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": "Hello!"}},
			},
		})
	}
	mux.HandleFunc("POST /v1/chat/completions", chat)
	mux.HandleFunc("POST /v1/messages", chat)

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"}) //nolint:errcheck
	})

	addr := "127.0.0.1:11000"
	fmt.Printf("[testserver] listening on http://%s\n", addr)
	fmt.Println("[testserver] routes: /ok  /slow  /error  /notfound  /health")
	fmt.Println("[testserver]         POST /v1/chat/completions  POST /v1/messages")
	log.Fatal(http.ListenAndServe(addr, mux))
}
