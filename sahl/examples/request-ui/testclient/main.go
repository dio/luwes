// testclient sends a representative mix of requests through the request-ui
// filter and prints a summary. Results appear in the web UI at
// http://localhost:6062/ in real time via SSE.
//
// Run (with Envoy started via `make run EXAMPLE=sahl/request-ui
// ENVOY_YAML=$(pwd)/sahl/examples/request-ui/envoy-local.yaml`):
//
//	go run ./sahl/examples/request-ui/testclient
//
// Scenarios covered:
//   - Normal 200 (white rows in UI)
//   - Upstream 500 (red, has_error=true)
//   - 404 (red, has_error=true)
//   - Slow 200 >500ms (yellow row)
//   - Cancelled request to /delayed: Envoy fault filter holds for 5s, client
//     cancels after 200ms, upstream never contacted. DC flag in response_flags.
//   - LLM-style POST with JSON body
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const base = "http://localhost:10000"

type result struct {
	method string
	path   string
	status int
	dur    time.Duration
	err    error
}

func main() {
	client := &http.Client{Timeout: 10 * time.Second}

	requests := []func() result{
		func() result { return get(client, "/ok") },
		func() result { return get(client, "/health") },
		func() result { return get(client, "/error") },
		func() result { return get(client, "/notfound") },
		func() result { return get(client, "/slow") },
		func() result {
			return post(client, "/v1/chat/completions", map[string]any{
				"model":    "gpt-4o-mini",
				"messages": []map[string]string{{"role": "user", "content": "hello"}},
			})
		},
		func() result {
			return post(client, "/v1/messages", map[string]any{
				"model":      "claude-3-haiku-20240307",
				"max_tokens": 100,
				"messages":   []map[string]string{{"role": "user", "content": "hello"}},
			})
		},
		// /delayed: Envoy fault filter holds the request for 5s before upstream.
		// Client cancels after 200ms: upstream never contacted, DC flag recorded.
		func() result { return cancelledGet(client, "/delayed", 200*time.Millisecond) },
		func() result { return get(client, "/ok") },
		func() result { return get(client, "/ok") },
	}

	fmt.Printf("%-6s %-35s %6s %8s\n", "METHOD", "PATH", "STATUS", "DUR")
	fmt.Println(repeat("-", 60))

	for _, req := range requests {
		r := req()
		if r.err != nil {
			fmt.Printf("%-6s %-35s  ERROR  %s\n", r.method, r.path, r.err)
			continue
		}
		statusStr := fmt.Sprintf("%d", r.status)
		if r.status == 0 {
			statusStr = "-"
		}
		fmt.Printf("%-6s %-35s %6s %7dms\n", r.method, r.path, statusStr, r.dur.Milliseconds())
	}

	fmt.Println(repeat("-", 60))
	fmt.Println("done: open http://localhost:6062/ to see all requests in the UI")
}

func get(c *http.Client, path string) result {
	start := time.Now()
	resp, err := c.Get(base + path)
	dur := time.Since(start)
	if err != nil {
		return result{"GET", path, 0, dur, err}
	}
	resp.Body.Close()
	return result{"GET", path, resp.StatusCode, dur, nil}
}

// cancelledGet sends a GET and cancels the context after timeout.
// Envoy records DC (downstream connection termination) in response_flags
// when the client disconnects before upstream is contacted.
func cancelledGet(c *http.Client, path string, timeout time.Duration) result {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	start := time.Now()
	resp, err := c.Do(req)
	dur := time.Since(start)
	if err != nil {
		return result{"GET", path + " (cancelled)", 0, dur, nil}
	}
	resp.Body.Close()
	return result{"GET", path, resp.StatusCode, dur, nil}
}

func post(c *http.Client, path string, body any) result {
	b, _ := json.Marshal(body)
	start := time.Now()
	resp, err := c.Post(base+path, "application/json", bytes.NewReader(b))
	dur := time.Since(start)
	if err != nil {
		return result{"POST", path, 0, dur, err}
	}
	resp.Body.Close()
	return result{"POST", path, resp.StatusCode, dur, nil}
}

func repeat(s string, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = s[0]
	}
	return string(b)
}
