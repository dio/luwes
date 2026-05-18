package sahl_test

import (
	"testing"

	"github.com/dio/luwes/sahl"
	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
)

func strBuf(s string) shared.UnsafeEnvoyBuffer {
	if s == "" {
		return shared.UnsafeEnvoyBuffer{}
	}
	b := []byte(s)
	return shared.UnsafeEnvoyBuffer{Ptr: &b[0], Len: uint64(len(b))}
}

// TestHTTPCallout_SuccessPath verifies:
//   - handler calls w.HTTPCallout, which returns HeadersStatusStop
//   - continuation fn fires with correct result/headers/body
//   - mutations queued inside fn are applied via flush
//   - ContinueRequest is called exactly once
func TestHTTPCallout_SuccessPath(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{
			":method": "GET",
			":path":   "/api",
			"x-token": "tok123",
		}),
		fake.WithHTTPCalloutFn(func(
			cluster string,
			headers [][2]string,
			body []byte,
			timeoutMs uint64,
			cb shared.HttpCalloutCallback,
		) (shared.HttpCalloutInitResult, uint64) {
			// Simulate Envoy: fire callback inline (same goroutine, worker thread).
			respHeaders := [][2]shared.UnsafeEnvoyBuffer{
				{strBuf("status"), strBuf("200")},
			}
			respBody := []shared.UnsafeEnvoyBuffer{strBuf(`{"allowed":true}`)}
			cb.OnHttpCalloutDone(1, shared.HttpCalloutSuccess, respHeaders, respBody)
			return shared.HttpCalloutInitSuccess, 1
		}),
	)

	var (
		gotResult  shared.HttpCalloutResult
		gotHeaders [][2]shared.UnsafeEnvoyBuffer
		gotBody    []shared.UnsafeEnvoyBuffer
	)

	filter := sahl.NewFilterForTesting(
		"test",
		func(w *sahl.Writer, r *sahl.Request) {
			w.HTTPCallout(sahl.HTTPCalloutRequest{
				Cluster: "auth",
				Headers: [][2]string{
					{"x-auth-token", r.Header.Get(":path")}, // just to exercise r.Header.Get
				},
				TimeoutMs: 250,
			}, func(result shared.HttpCalloutResult, hdrs [][2]shared.UnsafeEnvoyBuffer, bdy []shared.UnsafeEnvoyBuffer) {
				gotResult = result
				gotHeaders = hdrs
				gotBody = bdy
				w.SetRequestHeader("x-auth-verified", "true")
			})
		},
		nil,
		false,
		fh,
	)

	reqHeaders := fh.RequestHeaders().(*fake.FakeHeaderMap)

	status := filter.OnRequestHeaders(fh.RequestHeaders(), false)
	if status != shared.HeadersStatusStop {
		t.Fatalf("want HeadersStatusStop (callout in flight), got %d", status)
	}

	if gotResult != shared.HttpCalloutSuccess {
		t.Errorf("continuation: want HttpCalloutSuccess, got %d", gotResult)
	}
	if len(gotHeaders) == 0 {
		t.Error("continuation: want non-empty response headers")
	}
	if len(gotBody) == 0 {
		t.Error("continuation: want non-empty response body")
	}

	// Mutation was applied.
	if v := reqHeaders.GetString("x-auth-verified"); v != "true" {
		t.Errorf("SetRequestHeader: want %q, got %q", "true", v)
	}

	// ContinueRequest must have been called.
	if fh.ContinuedReq != 1 {
		t.Errorf("ContinueRequest: want 1 call, got %d", fh.ContinuedReq)
	}
}

// TestHTTPCallout_ClusterNotFound verifies that init failure is surfaced via
// HTTPCalloutE and the handler can short-circuit with w.Send.
func TestHTTPCallout_ClusterNotFound(t *testing.T) {
	fh := fake.NewFilterHandle() // default: returns ClusterNotFound

	filter := sahl.NewFilterForTesting(
		"test",
		func(w *sahl.Writer, r *sahl.Request) {
			init, err := w.HTTPCalloutE(sahl.HTTPCalloutRequest{
				Cluster:   "nonexistent",
				TimeoutMs: 250,
			}, func(_ shared.HttpCalloutResult, _ [][2]shared.UnsafeEnvoyBuffer, _ []shared.UnsafeEnvoyBuffer) {
				t.Error("continuation must not run if callout init failed")
			})
			if init != shared.HttpCalloutInitClusterNotFound {
				t.Errorf("want HttpCalloutInitClusterNotFound, got %d", init)
			}
			if err == nil {
				t.Error("want non-nil error for cluster not found")
			}
			w.Send(503, `{"error":"cluster not found"}`)
		},
		nil,
		false,
		fh,
	)

	status := filter.OnRequestHeaders(fh.RequestHeaders(), false)

	// Send was called: must stop, not forward.
	if status != shared.HeadersStatusStop {
		t.Errorf("want HeadersStatusStop (responded), got %d", status)
	}
	if fh.ContinuedReq != 0 {
		t.Errorf("ContinueRequest must not be called when Send was used, got %d", fh.ContinuedReq)
	}
}

// TestHTTPCallout_PanicOnDoubleCall ensures calling HTTPCallout twice panics.
func TestHTTPCallout_PanicOnDoubleCall(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithHTTPCalloutFn(func(
			_ string, _ [][2]string, _ []byte, _ uint64,
			_ shared.HttpCalloutCallback,
		) (shared.HttpCalloutInitResult, uint64) {
			// Do NOT fire callback: callout is still in-flight.
			return shared.HttpCalloutInitSuccess, 1
		}),
	)

	panicked := false
	filter := sahl.NewFilterForTesting(
		"test",
		func(w *sahl.Writer, r *sahl.Request) {
			w.HTTPCallout(sahl.HTTPCalloutRequest{Cluster: "auth", TimeoutMs: 10},
				func(_ shared.HttpCalloutResult, _ [][2]shared.UnsafeEnvoyBuffer, _ []shared.UnsafeEnvoyBuffer) {
				})
			func() {
				defer func() {
					if recover() != nil {
						panicked = true
					}
				}()
				w.HTTPCallout(sahl.HTTPCalloutRequest{Cluster: "auth2", TimeoutMs: 10},
					func(_ shared.HttpCalloutResult, _ [][2]shared.UnsafeEnvoyBuffer, _ []shared.UnsafeEnvoyBuffer) {
					})
			}()
		},
		nil,
		false,
		fh,
	)

	_ = filter.OnRequestHeaders(fh.RequestHeaders(), false)

	if !panicked {
		t.Error("expected panic on double HTTPCallout, none occurred")
	}
}
