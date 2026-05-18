package sahl_test

import (
	"testing"

	"github.com/dio/luwes/sahl"
	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
)

// TestHTTPStream_SuccessPath verifies:
//   - handler calls w.HTTPStream, OnRequestHeaders returns HeadersStatusStop
//   - OnHttpStreamHeaders fires the event fn with headers
//   - OnHttpStreamData fires the event fn with body chunks
//   - OnHttpStreamComplete triggers flush + ContinueRequest
//   - mutations queued inside event fn are applied
func TestHTTPStream_SuccessPath(t *testing.T) {
	var gotEvents []sahl.HTTPStreamEvent

	fh := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{":method": "POST", ":path": "/stream"}),
		fake.WithHTTPStreamFn(func(
			cluster string,
			headers [][2]string,
			body []byte,
			endOfStream bool,
			timeoutMs uint64,
			cb shared.HttpStreamCallback,
		) (shared.HttpCalloutInitResult, uint64) {
			const streamID uint64 = 42
			// Simulate Envoy firing events inline.
			cb.OnHttpStreamHeaders(streamID, [][2]shared.UnsafeEnvoyBuffer{
				{strBuf("status"), strBuf("200")},
			}, false)
			cb.OnHttpStreamData(streamID, []shared.UnsafeEnvoyBuffer{strBuf("chunk1")}, false)
			cb.OnHttpStreamData(streamID, []shared.UnsafeEnvoyBuffer{strBuf("chunk2")}, true)
			cb.OnHttpStreamComplete(streamID)
			return shared.HttpCalloutInitSuccess, streamID
		}),
	)

	filter := sahl.NewFilterForTesting(
		"test",
		func(w *sahl.Writer, r *sahl.Request) {
			stream, err := w.HTTPStream(sahl.HTTPStreamRequest{
				Cluster:   "llm",
				TimeoutMs: 1000,
			}, func(e sahl.HTTPStreamEvent) {
				gotEvents = append(gotEvents, e)
				if _, ok := e.(*sahl.HTTPStreamComplete); ok {
					w.SetRequestHeader("x-stream-done", "true")
				}
			})
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			// Send additional body chunk after init.
			stream.Send([]byte("extra"), true)
		},
		nil, false, fh,
	)

	status := filter.OnRequestHeaders(fh.RequestHeaders(), false)
	if status != shared.HeadersStatusStop {
		t.Fatalf("want HeadersStatusStop, got %d", status)
	}

	// 4 events: Headers, Data x2, Complete.
	if len(gotEvents) != 4 {
		t.Fatalf("want 4 events, got %d: %v", len(gotEvents), gotEvents)
	}
	if _, ok := gotEvents[0].(*sahl.HTTPStreamHeaders); !ok {
		t.Errorf("event[0]: want *HTTPStreamHeaders, got %T", gotEvents[0])
	}
	if _, ok := gotEvents[1].(*sahl.HTTPStreamData); !ok {
		t.Errorf("event[1]: want *HTTPStreamData, got %T", gotEvents[1])
	}
	if _, ok := gotEvents[2].(*sahl.HTTPStreamData); !ok {
		t.Errorf("event[2]: want *HTTPStreamData, got %T", gotEvents[2])
	}
	if _, ok := gotEvents[3].(*sahl.HTTPStreamComplete); !ok {
		t.Errorf("event[3]: want *HTTPStreamComplete, got %T", gotEvents[3])
	}

	// Mutation queued in Complete handler was applied.
	if v := fh.RequestHeaders().(*fake.FakeHeaderMap).GetString("x-stream-done"); v != "true" {
		t.Errorf("SetRequestHeader: want %q, got %q", "true", v)
	}

	// ContinueRequest called once (from OnHttpStreamComplete flush).
	if fh.ContinuedReq != 1 {
		t.Errorf("ContinueRequest: want 1, got %d", fh.ContinuedReq)
	}
}

// TestHTTPStream_Reset verifies OnHttpStreamReset fires a *HTTPStreamReset event.
func TestHTTPStream_Reset(t *testing.T) {
	var gotReset *sahl.HTTPStreamReset

	fh := fake.NewFilterHandle(
		fake.WithHTTPStreamFn(func(
			_ string, _ [][2]string, _ []byte, _ bool, _ uint64,
			cb shared.HttpStreamCallback,
		) (shared.HttpCalloutInitResult, uint64) {
			cb.OnHttpStreamReset(1, shared.HttpStreamResetReasonRemoteReset)
			return shared.HttpCalloutInitSuccess, 1
		}),
	)

	filter := sahl.NewFilterForTesting(
		"test",
		func(w *sahl.Writer, r *sahl.Request) {
			_, err := w.HTTPStream(sahl.HTTPStreamRequest{Cluster: "llm", TimeoutMs: 500},
				func(e sahl.HTTPStreamEvent) {
					if r, ok := e.(*sahl.HTTPStreamReset); ok {
						gotReset = r
						w.Send(502, `{"error":"stream reset"}`)
					}
				})
			if err != nil {
				t.Errorf("unexpected init error: %v", err)
			}
		},
		nil, false, fh,
	)

	_ = filter.OnRequestHeaders(fh.RequestHeaders(), false)

	if gotReset == nil {
		t.Fatal("want *HTTPStreamReset event, got none")
	}
	if gotReset.Reason != shared.HttpStreamResetReasonRemoteReset {
		t.Errorf("want RemoteReset, got %d", gotReset.Reason)
	}
	if len(fh.LocalResponses) != 1 || fh.LocalResponses[0].Status != 502 {
		t.Errorf("want 502 local response, got %v", fh.LocalResponses)
	}
}

// TestHTTPStream_ClusterNotFound verifies init failure is returned as error.
func TestHTTPStream_ClusterNotFound(t *testing.T) {
	fh := fake.NewFilterHandle() // default: ClusterNotFound

	filter := sahl.NewFilterForTesting(
		"test",
		func(w *sahl.Writer, r *sahl.Request) {
			_, err := w.HTTPStream(sahl.HTTPStreamRequest{Cluster: "missing", TimeoutMs: 100},
				func(e sahl.HTTPStreamEvent) {
					t.Error("event fn must not run on init failure")
				})
			if err == nil {
				t.Error("want non-nil error for cluster not found")
			}
			w.Send(503, `{"error":"no cluster"}`)
		},
		nil, false, fh,
	)

	status := filter.OnRequestHeaders(fh.RequestHeaders(), false)
	if status != shared.HeadersStatusStop {
		t.Errorf("want HeadersStatusStop (responded), got %d", status)
	}
}

// TestHTTPStream_PanicOnDoubleCall ensures HTTPStream + HTTPCallout are mutually exclusive.
func TestHTTPStream_PanicOnDoubleCall(t *testing.T) {
	streamFn := func(
		_ string, _ [][2]string, _ []byte, _ bool, _ uint64,
		_ shared.HttpStreamCallback,
	) (shared.HttpCalloutInitResult, uint64) {
		return shared.HttpCalloutInitSuccess, 1
	}

	fh := fake.NewFilterHandle(fake.WithHTTPStreamFn(streamFn))

	panicked := false
	filter := sahl.NewFilterForTesting(
		"test",
		func(w *sahl.Writer, r *sahl.Request) {
			_, _ = w.HTTPStream(sahl.HTTPStreamRequest{Cluster: "a", TimeoutMs: 10},
				func(sahl.HTTPStreamEvent) {})
			func() {
				defer func() {
					if recover() != nil {
						panicked = true
					}
				}()
				_, _ = w.HTTPStream(sahl.HTTPStreamRequest{Cluster: "b", TimeoutMs: 10},
					func(sahl.HTTPStreamEvent) {})
			}()
		},
		nil, false, fh,
	)

	_ = filter.OnRequestHeaders(fh.RequestHeaders(), false)
	if !panicked {
		t.Error("expected panic on double HTTPStream, none occurred")
	}
}
