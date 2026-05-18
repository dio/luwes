package sahl

import (
	"context"
	"errors"

	"github.com/dio/luwes/shared"
)

// HTTPCalloutResponse is the result returned by w.Do.
type HTTPCalloutResponse struct {
	Result  shared.HttpCalloutResult
	Headers [][2]shared.UnsafeEnvoyBuffer
	Body    []shared.UnsafeEnvoyBuffer
}

// errDoOutsideGo is the panic message when w.Do is called outside w.Go.
const errDoOutsideGo = "BUG: sahl: w.Do called outside w.Go: only valid inside a Go goroutine"

// Do performs a blocking HTTP callout to the given cluster.
// Must be called inside a w.Go goroutine: panics otherwise.
//
// Do schedules the callout on the Envoy worker thread via Scheduler.Schedule,
// blocks the calling goroutine until the response arrives or ctx is cancelled,
// then returns. Scheduling satisfies the ABI requirement that HttpCallout is
// only called from the worker thread or a scheduled function.
//
// Allocations per call: 1 channel + 1 closure (unavoidable for the sync bridge).
//
// # When to use Do vs HTTPCallout
//
// Use w.HTTPCallout (callback form) when:
//   - You need to reject the request via w.Send. SendLocalResponse is only
//     honored when called from a filter callback (OnRequestHeaders,
//     OnHttpCalloutDone, etc.), NOT from Scheduler.Schedule. Calling w.Send
//     inside w.Go or w.Do silently fails to deliver the local response.
//   - You want zero goroutine overhead.
//
// Use w.Do when:
//   - You have multiple sequential callouts and nested callbacks would hurt
//     readability.
//   - The filter always forwards the request (no rejection via w.Send).
//   - You can tolerate 1 goroutine + 1 channel per Do call.
//
// Example:
//
//	w.Go(func(ctx context.Context) {
//	    r1, err := w.Do(ctx, sahl.HTTPCalloutRequest{Cluster: "auth", ...})
//	    if err != nil {
//	        // Cannot w.Send here: SendLocalResponse from Scheduler.Schedule
//	        // is not honored by Envoy. Use w.HTTPCallout for rejection.
//	        return
//	    }
//	    r2, err := w.Do(ctx, sahl.HTTPCalloutRequest{Cluster: "quota", ...})
//	    if err != nil { return }
//	    w.SetRequestHeader("x-user", extractUser(r1))
//	    w.SetRequestHeader("x-quota", extractQuota(r2))
//	})
func (w *Writer) Do(ctx context.Context, req HTTPCalloutRequest) (*HTTPCalloutResponse, error) {
	if !w.goStarted {
		panic(errDoOutsideGo)
	}

	type result struct {
		resp *HTTPCalloutResponse
		err  error
	}

	ch := make(chan result, 1)

	// Schedule the HttpCallout on the worker thread. The ABI requires HttpCallout
	// to be called only from worker-thread callbacks or Scheduler.Schedule.
	w.scheduler.Schedule(func() {
		init, _ := w.handle.HttpCallout(
			req.Cluster, req.Headers, req.Body, req.TimeoutMs,
			httpCalloutCallbackFn(func(_ uint64, r shared.HttpCalloutResult, hdrs [][2]shared.UnsafeEnvoyBuffer, body []shared.UnsafeEnvoyBuffer) {
				ch <- result{resp: &HTTPCalloutResponse{Result: r, Headers: hdrs, Body: body}}
			}),
		)
		if init != shared.HttpCalloutInitSuccess {
			ch <- result{err: errCalloutInitResult(init)}
		}
	})

	select {
	case res := <-ch:
		return res.resp, res.err
	case <-ctx.Done():
		return nil, errors.New("sahl: w.Do: context cancelled: " + ctx.Err().Error())
	}
}

// httpCalloutCallbackFn adapts a closure to shared.HttpCalloutCallback.
type httpCalloutCallbackFn func(calloutID uint64, result shared.HttpCalloutResult, headers [][2]shared.UnsafeEnvoyBuffer, body []shared.UnsafeEnvoyBuffer)

func (f httpCalloutCallbackFn) OnHttpCalloutDone(calloutID uint64, result shared.HttpCalloutResult, headers [][2]shared.UnsafeEnvoyBuffer, body []shared.UnsafeEnvoyBuffer) {
	f(calloutID, result, headers, body)
}
