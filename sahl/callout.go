package sahl

import (
	"errors"
	"fmt"

	"github.com/dio/luwes/shared"
)

// HTTPCalloutRequest carries the parameters for an outbound HTTP callout.
// All fields except Cluster and TimeoutMs are optional.
type HTTPCalloutRequest struct {
	// Cluster is the Envoy cluster name to route the callout to. Required.
	Cluster string

	// Headers are additional request headers. The :method, :path, :authority,
	// and :scheme pseudo-headers must be included if the cluster expects them.
	Headers [][2]string

	// Body is the optional request body.
	Body []byte

	// TimeoutMs is the callout timeout in milliseconds. 0 means Envoy default.
	TimeoutMs uint64
}

// HTTPCalloutFunc is the continuation called on the Envoy worker thread when
// the callout completes. It runs synchronously with no goroutine overhead.
type HTTPCalloutFunc func(
	result shared.HttpCalloutResult,
	headers [][2]shared.UnsafeEnvoyBuffer,
	body []shared.UnsafeEnvoyBuffer,
)

// errCalloutInitResult maps HttpCalloutInitResult to a human-readable error.
func errCalloutInitResult(r shared.HttpCalloutInitResult) error {
	switch r {
	case shared.HttpCalloutInitSuccess:
		return nil
	case shared.HttpCalloutInitMissingRequiredHeaders:
		return errors.New("sahl: HTTPCallout: missing required headers")
	case shared.HttpCalloutInitClusterNotFound:
		return errors.New("sahl: HTTPCallout: cluster not found")
	case shared.HttpCalloutInitDuplicateCalloutId:
		return errors.New("sahl: HTTPCallout: duplicate callout ID")
	case shared.HttpCalloutInitCannotCreateRequest:
		return errors.New("sahl: HTTPCallout: cannot create request")
	default:
		return fmt.Errorf("sahl: HTTPCallout: unknown init result %d", r)
	}
}

// HTTPCallout initiates an outbound HTTP callout to the given cluster.
// The handler must return immediately after calling HTTPCallout; Envoy
// pauses the request (HeadersStatusStop is returned from OnRequestHeaders).
// fn is called on the Envoy worker thread when the response arrives.
//
// HTTPCallout panics if the callout was already initiated (duplicate call),
// or if w.Go was already called on this request.
//
// On init failure (cluster not found, missing headers, etc.) the callout is
// not initiated and fn is never called. Use HTTPCalloutE to inspect the init
// result and handle failures explicitly.
func (w *Writer) HTTPCallout(req HTTPCalloutRequest, fn HTTPCalloutFunc) {
	_, _ = w.HTTPCalloutE(req, fn)
}

// HTTPCalloutE is like HTTPCallout but returns the init result and a non-nil
// error if the callout could not be initiated. fn is never called on failure.
//
//	init, err := w.HTTPCalloutE(sahl.HTTPCalloutRequest{Cluster: "auth", TimeoutMs: 250}, func(...) {
//	    // runs on worker thread if init succeeded
//	})
//	if err != nil {
//	    w.Send(503, `{"error":"upstream unavailable"}`)
//	    return
//	}
func (w *Writer) HTTPCalloutE(req HTTPCalloutRequest, fn HTTPCalloutFunc) (shared.HttpCalloutInitResult, error) {
	if w.calloutStarted {
		panic("BUG: sahl: HTTPCallout called twice on the same request")
	}
	if w.goStarted {
		panic("BUG: sahl: HTTPCallout and Go are mutually exclusive on the same request")
	}

	// Store the continuation before initiating; Envoy may fire the callback
	// inline (same goroutine) before HttpCallout returns.
	w.calloutFn = fn
	init, _ := w.handle.HttpCallout(
		req.Cluster, req.Headers, req.Body, req.TimeoutMs, w.calloutCB,
	)
	if init != shared.HttpCalloutInitSuccess {
		// Clear stored fn: it will never be called.
		w.calloutFn = nil
		return init, errCalloutInitResult(init)
	}
	w.calloutStarted = true
	return shared.HttpCalloutInitSuccess, nil
}
