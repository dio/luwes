package sahl

import (
	"testing"
	"unsafe"

	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
)

func testBuf(s string) shared.UnsafeEnvoyBuffer {
	if s == "" {
		return shared.UnsafeEnvoyBuffer{}
	}
	b := []byte(s)
	return shared.UnsafeEnvoyBuffer{Ptr: (*byte)(unsafe.Pointer(&b[0])), Len: uint64(len(b))}
}

// TestStreamDone_CalloutFnNotCalledAfterDisconnect verifies that if the
// downstream client disconnects (OnStreamComplete fires) while a callout is
// in-flight, a late-firing OnHttpCalloutDone does not invoke the continuation
// fn, does not call ContinueRequest, and does not panic.
func TestStreamDone_CalloutFnNotCalledAfterDisconnect(t *testing.T) {
	var calloutCBStored shared.HttpCalloutCallback

	fh := fake.NewFilterHandle(
		fake.WithHTTPCalloutFn(func(
			_ string, _ [][2]string, _ []byte, _ uint64,
			cb shared.HttpCalloutCallback,
		) (shared.HttpCalloutInitResult, uint64) {
			// Store the callback but do NOT fire it yet: callout is in-flight.
			calloutCBStored = cb
			return shared.HttpCalloutInitSuccess, 1
		}),
	)

	fnCalled := false
	filter := NewFilterForTesting(
		"test",
		func(w *Writer, r *Request) {
			w.HTTPCallout(HTTPCalloutRequest{Cluster: "auth", TimeoutMs: 500},
				func(_ shared.HttpCalloutResult, _ [][2]shared.UnsafeEnvoyBuffer, _ []shared.UnsafeEnvoyBuffer) {
					fnCalled = true
				})
		},
		nil, false, fh,
	)

	// Handler pauses the request.
	status := filter.OnRequestHeaders(fh.RequestHeaders(), false)
	if status != shared.HeadersStatusStop {
		t.Fatalf("want HeadersStatusStop, got %d", status)
	}
	if calloutCBStored == nil {
		t.Fatal("callout callback was not stored by fake")
	}

	// Downstream disconnects before the callout completes.
	filter.OnStreamComplete()

	// Envoy fires the callout callback late (after disconnect).
	calloutCBStored.OnHttpCalloutDone(1, shared.HttpCalloutSuccess,
		[][2]shared.UnsafeEnvoyBuffer{{testBuf("status"), testBuf("200")}},
		nil,
	)

	// Continuation fn must NOT have been called.
	if fnCalled {
		t.Error("continuation fn must not be called after OnStreamComplete")
	}
	// ContinueRequest must NOT have been called on a dead stream.
	if fh.ContinuedReq != 0 {
		t.Errorf("ContinueRequest must not be called after OnStreamComplete, got %d", fh.ContinuedReq)
	}
}

// TestStreamDone_StreamCompleteFnNotCalledAfterDisconnect verifies the same
// guard for OnHttpStreamComplete.
func TestStreamDone_StreamCompleteFnNotCalledAfterDisconnect(t *testing.T) {
	var streamCBStored shared.HttpStreamCallback

	fh := fake.NewFilterHandle(
		fake.WithHTTPStreamFn(func(
			_ string, _ [][2]string, _ []byte, _ bool, _ uint64,
			cb shared.HttpStreamCallback,
		) (shared.HttpCalloutInitResult, uint64) {
			streamCBStored = cb
			return shared.HttpCalloutInitSuccess, 1
		}),
	)

	completeCalled := false
	filter := NewFilterForTesting(
		"test",
		func(w *Writer, r *Request) {
			_, _ = w.HTTPStream(HTTPStreamRequest{Cluster: "llm", TimeoutMs: 500},
				func(e HTTPStreamEvent) {
					if _, ok := e.(*HTTPStreamComplete); ok {
						completeCalled = true
					}
				})
		},
		nil, false, fh,
	)

	status := filter.OnRequestHeaders(fh.RequestHeaders(), false)
	if status != shared.HeadersStatusStop {
		t.Fatalf("want HeadersStatusStop, got %d", status)
	}
	if streamCBStored == nil {
		t.Fatal("stream callback was not stored by fake")
	}

	// Downstream disconnects.
	filter.OnStreamComplete()

	// Envoy fires stream complete late.
	streamCBStored.OnHttpStreamComplete(1)

	if completeCalled {
		t.Error("stream Complete event fn must not be called after OnStreamComplete")
	}
	if fh.ContinuedReq != 0 {
		t.Errorf("ContinueRequest must not be called after OnStreamComplete, got %d", fh.ContinuedReq)
	}
}

// TestStreamDone_StreamResetFnNotCalledAfterDisconnect verifies the same
// guard for OnHttpStreamReset.
func TestStreamDone_StreamResetFnNotCalledAfterDisconnect(t *testing.T) {
	var streamCBStored shared.HttpStreamCallback

	fh := fake.NewFilterHandle(
		fake.WithHTTPStreamFn(func(
			_ string, _ [][2]string, _ []byte, _ bool, _ uint64,
			cb shared.HttpStreamCallback,
		) (shared.HttpCalloutInitResult, uint64) {
			streamCBStored = cb
			return shared.HttpCalloutInitSuccess, 1
		}),
	)

	resetCalled := false
	filter := NewFilterForTesting(
		"test",
		func(w *Writer, r *Request) {
			_, _ = w.HTTPStream(HTTPStreamRequest{Cluster: "llm", TimeoutMs: 500},
				func(e HTTPStreamEvent) {
					if _, ok := e.(*HTTPStreamReset); ok {
						resetCalled = true
					}
				})
		},
		nil, false, fh,
	)

	status := filter.OnRequestHeaders(fh.RequestHeaders(), false)
	if status != shared.HeadersStatusStop {
		t.Fatalf("want HeadersStatusStop, got %d", status)
	}

	// Downstream disconnects.
	filter.OnStreamComplete()

	// Envoy fires reset late.
	streamCBStored.OnHttpStreamReset(1, shared.HttpStreamResetReasonRemoteReset)

	if resetCalled {
		t.Error("stream Reset event fn must not be called after OnStreamComplete")
	}
}
