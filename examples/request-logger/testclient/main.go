// testclient sends a representative mix of requests through the request-logger
// filter and prints a summary of what it sent.
//
// Run (with Envoy already started on :10000):
//
//	go run ./examples/request-logger/testclient
//
// What it sends:
//   - Normal GET and POST requests (200)
//   - A slow request (exercises duration logging)
//   - An upstream 500 (exercises error_details / response_flags)
//   - A 404 (exercises NR / response_code_details)
//   - LLM-style POST with JSON body and model routing headers
//   - A cancelled request to /delayed: Envoy holds the request for 5s via fault
//     injection; the client cancels after 200ms so upstream is never contacted.
//     Envoy records DC (downstream connection termination) in response_flags.
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
			return post(client, "/v1/chat/completions",
				map[string]any{
					"model":    "gpt-4o-mini",
					"messages": []map[string]string{{"role": "user", "content": "hello"}},
				},
			)
		},
		func() result {
			return post(client, "/v1/messages",
				map[string]any{
					"model":      "claude-3-haiku-20240307",
					"max_tokens": 100,
					"messages":   []map[string]string{{"role": "user", "content": "hello"}},
				},
			)
		},
		// /delayed: Envoy fault filter holds the request for 5s before hitting
		// upstream. We cancel after 200ms so upstream is never contacted.
		// Envoy records DC (downstream connection termination) in response_flags.
		func() result { return cancelledGet(client, "/delayed", 200*time.Millisecond) },
		func() result { return get(client, "/ok") },
		func() result { return get(client, "/ok") },
	}

	fmt.Printf("%-6s %-30s %6s %8s\n", "METHOD", "PATH", "STATUS", "DUR")
	fmt.Println(repeat("-", 55))

	for _, req := range requests {
		r := req()
		if r.err != nil {
			fmt.Printf("%-6s %-30s  ERROR  %s\n", r.method, r.path, r.err)
			continue
		}
		fmt.Printf("%-6s %-30s %6d %7dms\n", r.method, r.path, r.status, r.dur.Milliseconds())
	}

	fmt.Println(repeat("-", 55))
	fmt.Println("done: check Envoy stdout for JSON access log records")
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
// The server (Envoy) sees a downstream disconnect (DC flag) and never contacts
// the upstream if the cancellation arrives before the fault delay expires.
func cancelledGet(c *http.Client, path string, timeout time.Duration) result {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	start := time.Now()
	resp, err := c.Do(req)
	dur := time.Since(start)
	if err != nil {
		// context.DeadlineExceeded is expected: we cancelled intentionally.
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
