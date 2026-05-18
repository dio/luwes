// Package accessloggersink is a test-only HTTP sink for access log entries.
// The e2e access logger module POSTs JSON entries to a local HTTP server
// started by the test harness. This avoids the cross-.so package isolation
// problem: the Go runtime in libe2e.so and the test binary are separate
// address spaces, so a shared channel cannot work.
//
// Usage in tests: call StartSink() in TestMain to get the sink URL, pass it
// to Envoy via the access logger config, then call Drain() in tests.
package accessloggersink

import (
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"time"
)

// Entry is one access log event delivered from the module under test.
type Entry struct {
	LogType       int32   `json:"log_type"`
	DurationMs    float64 `json:"duration_ms"`
	BytesSent     uint64  `json:"bytes_sent"`
	BytesReceived uint64  `json:"bytes_received"`
	ResponseCode  uint32  `json:"response_code"`
	ResponseFlags uint64  `json:"response_flags"`
	CodeDetails   string  `json:"code_details"`
	RequestID     string  `json:"request_id"`
}

var (
	mu      sync.Mutex
	entries []Entry
	notify  chan struct{}
)

func init() {
	notify = make(chan struct{}, 64)
}

// StartSink starts a local HTTP server that receives access log entries POSTed
// as JSON from the stub logger. Returns the base URL (e.g. "http://127.0.0.1:PORT").
func StartSink() string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic("accessloggersink: listen failed: " + err.Error())
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/log", func(w http.ResponseWriter, r *http.Request) {
		var e Entry
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			http.Error(w, "bad json", 400)
			return
		}
		mu.Lock()
		entries = append(entries, e)
		mu.Unlock()
		select {
		case notify <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck
	return "http://" + ln.Addr().String()
}

// Drain collects all entries received within timeout.
// Blocks until at least one entry arrives or timeout expires.
func Drain(timeout time.Duration) []Entry {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case <-notify:
			mu.Lock()
			out := make([]Entry, len(entries))
			copy(out, entries)
			mu.Unlock()
			return out
		case <-deadline.C:
			mu.Lock()
			out := make([]Entry, len(entries))
			copy(out, entries)
			mu.Unlock()
			return out
		}
	}
}

// Reset discards all pending entries. Call at the start of each test.
func Reset() {
	mu.Lock()
	entries = entries[:0]
	mu.Unlock()
	// Drain notify channel.
	for {
		select {
		case <-notify:
		default:
			return
		}
	}
}
