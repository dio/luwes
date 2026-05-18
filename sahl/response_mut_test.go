package sahl_test

import (
	"testing"

	"github.com/dio/luwes/sahl"
	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
)

// TestResponseFlags_PopulatedFromHeader verifies ResponseChunk.ResponseFlags
// is populated from the x-envoy-response-flags response header.
func TestResponseFlags_PopulatedFromHeader(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithResponseHeaders(map[string]string{
			":status":                "503",
			"content-type":           "text/plain",
			"x-envoy-response-flags": "UF,UT",
		}),
	)

	var gotFlags string
	filter := sahl.NewFilterForTesting(
		"test", func(w *sahl.Writer, r *sahl.Request) {}, func(w *sahl.Writer, chunk *sahl.ResponseChunk) {
			if chunk.Data == nil {
				gotFlags = chunk.ResponseFlags
			}
		}, false, fh,
	)

	filter.OnRequestHeaders(fh.RequestHeaders(), false)
	filter.OnResponseHeaders(fh.ResponseHeaders(), false)

	if gotFlags != "UF,UT" {
		t.Errorf("want ResponseFlags=%q, got %q", "UF,UT", gotFlags)
	}
}

// TestResponseFlags_EmptyWhenAbsent verifies ResponseChunk.ResponseFlags is
// empty string when the header is absent.
func TestResponseFlags_EmptyWhenAbsent(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithResponseHeaders(map[string]string{":status": "200"}),
	)

	var gotFlags string
	filter := sahl.NewFilterForTesting(
		"test", func(w *sahl.Writer, r *sahl.Request) {}, func(w *sahl.Writer, chunk *sahl.ResponseChunk) {
			gotFlags = chunk.ResponseFlags
		}, false, fh,
	)

	filter.OnRequestHeaders(fh.RequestHeaders(), false)
	filter.OnRequestHeaders(fh.RequestHeaders(), false)
	filter.OnResponseHeaders(fh.ResponseHeaders(), false)

	if gotFlags != "" {
		t.Errorf("want empty ResponseFlags, got %q", gotFlags)
	}
}

// TestMutableResponse_SetResponseBody verifies w.SetResponseBody replaces the
// entire buffered response body on EndStream.
func TestMutableResponse_SetResponseBody(t *testing.T) {
	original := []byte(`{"original":true}`)
	replacement := []byte(`{"mutated":true}`)

	fh := fake.NewFilterHandle(
		fake.WithResponseHeaders(map[string]string{
			":status":      "200",
			"content-type": "application/json",
		}),
		fake.WithResponseBody(original),
	)

	filter := sahl.NewFilterForTesting(
		"test",
		func(w *sahl.Writer, r *sahl.Request) {},
		func(w *sahl.Writer, chunk *sahl.ResponseChunk) {
			if chunk.EndStream {
				w.SetResponseBody(replacement)
			}
		},
		false, fh,
	)
	// Mark filter as mutable-response mode.
	filter.SetMutableResponse(true)

	// Initialise writer (required before response phase).
	filter.OnRequestHeaders(fh.RequestHeaders(), false)
	filter.OnResponseHeaders(fh.ResponseHeaders(), false)
	filter.OnResponseBody(fh.BufferedResponseBody().(*fake.FakeBodyBuffer), true)

	// After OnResponseBody(endStream=true), the body must be replaced.
	got := fh.BufferedResponseBody().(*fake.FakeBodyBuffer).Body
	if string(got) != string(replacement) {
		t.Errorf("want body %q, got %q", replacement, got)
	}
}

// TestMutableResponse_AppendResponseBody verifies w.AppendResponseBody appends
// to the existing buffered response body.
func TestMutableResponse_AppendResponseBody(t *testing.T) {
	original := []byte(`{"a":1}`)
	suffix := []byte(`{"b":2}`)

	fh := fake.NewFilterHandle(
		fake.WithResponseHeaders(map[string]string{":status": "200"}),
		fake.WithResponseBody(original),
	)

	filter := sahl.NewFilterForTesting(
		"test",
		func(w *sahl.Writer, r *sahl.Request) {},
		func(w *sahl.Writer, chunk *sahl.ResponseChunk) {
			if chunk.EndStream {
				w.AppendResponseBody(suffix)
			}
		},
		false, fh,
	)
	filter.SetMutableResponse(true)

	// Initialise writer (required before response phase).
	filter.OnRequestHeaders(fh.RequestHeaders(), false)
	filter.OnResponseHeaders(fh.ResponseHeaders(), false)
	respBody := fh.BufferedResponseBody().(*fake.FakeBodyBuffer)
	filter.OnResponseBody(respBody, true)

	want := string(original) + string(suffix)
	if string(respBody.Body) != want {
		t.Errorf("want body %q, got %q", want, respBody.Body)
	}
}

// TestMutableResponse_StopAndBufferOnBodyChunks verifies that a mutable-response
// filter returns BodyStatusStopAndBuffer on non-final chunks.
func TestMutableResponse_StopAndBufferOnBodyChunks(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithResponseHeaders(map[string]string{":status": "200"}),
		fake.WithResponseBody([]byte("chunk")),
	)

	filter := sahl.NewFilterForTesting(
		"test",
		func(w *sahl.Writer, r *sahl.Request) {},
		func(w *sahl.Writer, chunk *sahl.ResponseChunk) {},
		false, fh,
	)
	filter.SetMutableResponse(true)

	// Initialise writer (required before response phase).
	filter.OnRequestHeaders(fh.RequestHeaders(), false)
	filter.OnResponseHeaders(fh.ResponseHeaders(), false)
	respBody := fh.BufferedResponseBody().(*fake.FakeBodyBuffer)

	// Non-final chunk: must stop and buffer.
	status := filter.OnResponseBody(respBody, false)
	if status != shared.BodyStatusStopAndBuffer {
		t.Errorf("want BodyStatusStopAndBuffer on non-final chunk, got %d", status)
	}

	// Final chunk: must continue (after applying mutations).
	status = filter.OnResponseBody(respBody, true)
	if status != shared.BodyStatusContinue {
		t.Errorf("want BodyStatusContinue on final chunk, got %d", status)
	}
}

// TestObserveOnly_StillContinues verifies that filters registered without
// mutable-response mode still return BodyStatusContinue (streaming preserved).
func TestObserveOnly_StillContinues(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithResponseHeaders(map[string]string{":status": "200"}),
		fake.WithResponseBody([]byte("data")),
	)

	filter := sahl.NewFilterForTesting(
		"test",
		func(w *sahl.Writer, r *sahl.Request) {},
		func(w *sahl.Writer, chunk *sahl.ResponseChunk) {},
		false, fh,
	)
	// No SetMutableResponse: observe-only mode.

	filter.OnRequestHeaders(fh.RequestHeaders(), false)
	filter.OnResponseHeaders(fh.ResponseHeaders(), false)
	respBody := fh.BufferedResponseBody().(*fake.FakeBodyBuffer)

	status := filter.OnResponseBody(respBody, false)
	if status != shared.BodyStatusContinue {
		t.Errorf("want BodyStatusContinue for observe-only filter, got %d", status)
	}
}
