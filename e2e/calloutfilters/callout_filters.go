package calloutfilters

// Package calloutfilters registers three sahl filters exercised by the e2e
// callout test suite:
//
//   - callout-sahl:  uses w.HTTPCallout to call the callout_upstream cluster.
//     Injects x-auth-user from the callout response or returns 401.
//   - stream-sahl:   uses w.HTTPStream to stream to callout_upstream.
//     Counts data chunks, returns 502 on stream reset.
//   - do-sahl:       uses w.Go + w.Do. Same contract as callout-sahl.

import (
	"context"
	"fmt"
	"strconv"

	"github.com/dio/luwes/sahl"
	"github.com/dio/luwes/shared"
)

// calloutHandler uses w.HTTPCallout (CPS, 0 goroutines).
// The callout target is the callout_upstream cluster.
// Request path is forwarded as the callout path so the mock upstream can vary
// the response per path (/auth-ok -> 200, /auth-deny -> 401).
func calloutHandler(w *sahl.Writer, r *sahl.Request) {
	path := r.Header.Get(":path")
	w.HTTPCallout(sahl.HTTPCalloutRequest{
		Cluster: "callout_upstream",
		Headers: [][2]string{
			{":method", "GET"},
			{":path", path},
			{":scheme", "http"},
			{":authority", "callout_upstream"},
		},
		TimeoutMs: 2000,
	}, func(result shared.HttpCalloutResult, hdrs [][2]shared.UnsafeEnvoyBuffer, body []shared.UnsafeEnvoyBuffer) {
		if result != shared.HttpCalloutSuccess {
			w.Send(502, `{"error":"callout failed"}`)
			return
		}
		// Read x-auth-user from callout response headers.
		status := ""
		user := ""
		for _, h := range hdrs {
			k := h[0].ToString()
			v := h[1].ToString()
			if k == ":status" || k == "status" {
				status = v
			}
			if k == "x-auth-user" {
				user = v
			}
		}
		if status == "401" {
			w.Send(401, `{"error":"denied"}`)
			return
		}
		if user != "" {
			w.SetRequestHeader("x-auth-user", user)
		}
	})
}

// streamHandler uses w.HTTPStream (bidirectional, 0 goroutines).
// Streams to callout_upstream, counts OnHttpStreamData chunks, sets
// x-stream-chunks on the forwarded request. Returns 502 on reset.
func streamHandler(w *sahl.Writer, r *sahl.Request) {
	path := r.Header.Get(":path")
	chunks := 0

	stream, err := w.HTTPStream(sahl.HTTPStreamRequest{
		Cluster: "callout_upstream",
		Headers: [][2]string{
			{":method", "POST"},
			{":path", path},
			{":scheme", "http"},
			{":authority", "callout_upstream"},
			{"content-type", "application/json"},
		},
		TimeoutMs:   2000,
		EndOfStream: false,
	}, func(e sahl.HTTPStreamEvent) {
		switch ev := e.(type) {
		case *sahl.HTTPStreamData:
			chunks++
			_ = ev
		case *sahl.HTTPStreamComplete:
			w.SetRequestHeader("x-stream-chunks", strconv.Itoa(chunks))
		case *sahl.HTTPStreamReset:
			w.Send(502, fmt.Sprintf(`{"error":"reset","reason":%d}`, ev.Reason))
		}
	})
	if err != nil {
		w.Send(503, `{"error":"stream init failed"}`)
		return
	}
	// Send request body and close the request side.
	stream.Send([]byte(`{"hello":"stream"}`), true)
}

// doHandler uses w.Go + w.Do (1 goroutine, channel bridge).
// Same external contract as calloutHandler.
func doHandler(w *sahl.Writer, r *sahl.Request) {
	path := r.Header.Get(":path")
	w.Go(func(ctx context.Context) {
		resp, err := w.Do(ctx, sahl.HTTPCalloutRequest{
			Cluster: "callout_upstream",
			Headers: [][2]string{
				{":method", "GET"},
				{":path", path},
				{":scheme", "http"},
				{":authority", "callout_upstream"},
			},
			TimeoutMs: 2000,
		})
		if err != nil {
			w.Send(502, `{"error":"do failed"}`)
			return
		}
		status := ""
		user := ""
		for _, h := range resp.Headers {
			k := h[0].ToString()
			v := h[1].ToString()
			if k == ":status" || k == "status" {
				status = v
			}
			if k == "x-auth-user" {
				user = v
			}
		}
		if status == "401" {
			w.Send(401, `{"error":"denied"}`)
			return
		}
		if user != "" {
			w.SetRequestHeader("x-auth-user", user)
		}
	})
}

func init() {
	sahl.Register("callout-sahl", calloutHandler)
	sahl.Register("stream-sahl", streamHandler)
	sahl.Register("do-sahl", doHandler)
}
