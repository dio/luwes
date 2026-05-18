package llmproxy

import (
	"testing"

	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
)

// TestFilter_ModelRouting verifies that a POST body with a known model name
// causes the correct x-cluster header to be set and ClearRouteCache to be called.
func TestFilter_ModelRouting(t *testing.T) {
	cases := []struct {
		body    string
		cluster string
	}{
		{`{"model":"gpt-4","messages":[]}`, "openai"},
		{`{"model":"claude-3-sonnet","messages":[]}`, "anthropic"},
		{`{"model":"gemini-pro","messages":[]}`, "default"},
		{`{"messages":[]}`, "default"}, // no model field
	}

	for _, c := range cases {
		fh := fake.NewFilterHandle(
			fake.WithRequestBody([]byte(c.body)),
		)
		factory, err := NewFactory(nil, nil)
		if err != nil {
			t.Fatalf("NewFactory: %v", err)
		}
		filter := factory.Create(fh)

		filter.OnRequestHeaders(fh.RequestHeaders(), false)
		filter.OnRequestBody(fh.BufferedRequestBody(), true)

		got := fh.RequestHeaders().(*fake.FakeHeaderMap).GetString("x-cluster")
		if got != c.cluster {
			t.Errorf("body=%q: want x-cluster=%q, got %q", c.body, c.cluster, got)
		}
		if fh.ClearedRouteCache == 0 {
			t.Errorf("body=%q: ClearRouteCache not called", c.body)
		}
		filter.OnStreamComplete()
	}
}

// TestFilter_StopAndBufferUntilEndStream verifies non-final body chunks
// return BodyStatusStopAndBuffer.
func TestFilter_StopAndBufferUntilEndStream(t *testing.T) {
	fh := fake.NewFilterHandle()
	factory, _ := NewFactory(nil, nil)
	filter := factory.Create(fh)

	filter.OnRequestHeaders(fh.RequestHeaders(), false)

	chunk := fake.NewFakeBodyBuffer([]byte(`{"model":"gpt`))
	status := filter.OnRequestBody(chunk, false)
	if status != shared.BodyStatusStopAndBuffer {
		t.Errorf("want BodyStatusStopAndBuffer, got %d", status)
	}

	full := fake.NewFakeBodyBuffer([]byte(`{"model":"gpt-4","messages":[]}`))
	status = filter.OnRequestBody(full, true)
	if status != shared.BodyStatusContinue {
		t.Errorf("want BodyStatusContinue on endStream, got %d", status)
	}
	filter.OnStreamComplete()
}

// TestFilter_SSESkipped verifies non-SSE responses are not tapped.
func TestFilter_SSESkipped(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithResponseHeaders(map[string]string{
			":status":      "200",
			"content-type": "application/json",
		}),
		fake.WithResponseBody([]byte(`{"id":"x","object":"chat.completion"}`)),
	)
	factory, _ := NewFactory(nil, nil)
	filter := factory.Create(fh)

	filter.OnRequestHeaders(fh.RequestHeaders(), false)
	filter.OnRequestBody(fake.NewFakeBodyBuffer([]byte(`{"model":"gpt-4"}`)), true)
	filter.OnResponseHeaders(fh.ResponseHeaders(), false)
	status := filter.OnResponseBody(fh.BufferedResponseBody(), true)
	if status != shared.BodyStatusContinue {
		t.Errorf("want BodyStatusContinue for non-SSE, got %d", status)
	}
	filter.OnStreamComplete()
}

// BenchmarkLLMProxy_ModelRouting benchmarks the hot path:
// OnRequestBody with a known model, zero allocs expected on the fake.
// Uses BenchFilterHandle (silent header mutations) and SilentBodyBuffer
// (pre-allocated GetChunks) to eliminate fake recording noise.
func BenchmarkLLMProxy_ModelRouting(b *testing.B) {
	bodyBytes := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	body := fake.NewSilentBodyBuffer(bodyBytes)
	fh := fake.NewBenchFilterHandle(
		fake.WithRequestBody(bodyBytes),
	)

	factory, _ := NewFactory(nil, nil)

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		filter := factory.Create(fh)
		filter.OnRequestHeaders(fh.RequestHeaders(), false)
		filter.OnRequestBody(body, true)
		filter.OnStreamComplete()
	}
}
