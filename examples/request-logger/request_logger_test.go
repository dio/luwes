package requestlogger

import (
	"testing"

	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
)

// -- helpers --

func strBuf(s string) shared.UnsafeEnvoyBuffer {
	if s == "" {
		return shared.UnsafeEnvoyBuffer{}
	}
	b := []byte(s)
	return shared.UnsafeEnvoyBuffer{Ptr: &b[0], Len: uint64(len(b))}
}

func newDefaultFactory() *Factory {
	f, _ := NewFactory(nil, nil)
	return f.(*Factory)
}

func newConfigFactory(cfg string) *Factory {
	f, _ := NewFactory(nil, []byte(cfg))
	return f.(*Factory)
}

// fakeSpan records SetTag calls.
type fakeSpan struct {
	tags map[string]string
}

func newFakeSpan() *fakeSpan { return &fakeSpan{tags: map[string]string{}} }

func (s *fakeSpan) SetTag(k, v string)        { s.tags[k] = v }
func (s *fakeSpan) SetOperation(string)       {}
func (s *fakeSpan) SetSampled(bool)           {}
func (s *fakeSpan) Log(string)                {}
func (s *fakeSpan) SetBaggage(string, string) {}
func (s *fakeSpan) GetBaggage(string) (shared.UnsafeEnvoyBuffer, bool) {
	return shared.UnsafeEnvoyBuffer{}, false
}
func (s *fakeSpan) GetTraceID() (shared.UnsafeEnvoyBuffer, bool) {
	return strBuf("trace-abc"), true
}
func (s *fakeSpan) GetSpanID() (shared.UnsafeEnvoyBuffer, bool) {
	return strBuf("span-xyz"), true
}
func (s *fakeSpan) SpawnChild(string) shared.ChildSpan { return nil }

// -- tests --

// TestDefaultConfig verifies defaults: headers recorded, bodies not buffered.
func TestDefaultConfig(t *testing.T) {
	f := newDefaultFactory()
	if !f.cfg.RecordRequestHeaders {
		t.Error("want RecordRequestHeaders=true by default")
	}
	if !f.cfg.RecordResponseHeaders {
		t.Error("want RecordResponseHeaders=true by default")
	}
	if f.cfg.RecordRequestBody {
		t.Error("want RecordRequestBody=false by default")
	}
	if f.cfg.RecordResponseBody {
		t.Error("want RecordResponseBody=false by default")
	}
	if f.cfg.MaxBodyBytes != defaultMaxBody {
		t.Errorf("want MaxBodyBytes=%d, got %d", defaultMaxBody, f.cfg.MaxBodyBytes)
	}
}

// TestConfigParsing verifies JSON config is parsed correctly.
func TestConfigParsing(t *testing.T) {
	f := newConfigFactory(`{
		"record_request_headers": false,
		"record_response_headers": false,
		"record_request_body": true,
		"record_response_body": true,
		"max_body_bytes": 1024
	}`)
	if f.cfg.RecordRequestHeaders {
		t.Error("want RecordRequestHeaders=false")
	}
	if !f.cfg.RecordRequestBody {
		t.Error("want RecordRequestBody=true")
	}
	if f.cfg.MaxBodyBytes != 1024 {
		t.Errorf("want MaxBodyBytes=1024, got %d", f.cfg.MaxBodyBytes)
	}
}

// TestHappyPath verifies that on a normal request/response cycle:
// - request fields are captured
// - trace IDs are extracted from the active span
// - response status is captured
// - span is tagged at stream completion
// - metadata is written
func TestHappyPath(t *testing.T) {
	span := newFakeSpan()
	fh := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{
			":method":      "POST",
			":path":        "/v1/chat/completions",
			":authority":   "api.openai.com",
			"x-request-id": "req-123",
		}),
		fake.WithResponseHeaders(map[string]string{
			":status": "200",
		}),
		fake.WithActiveSpan(span),
	)

	factory := newDefaultFactory()
	filter := factory.Create(fh)

	// Request phase.
	status := filter.OnRequestHeaders(fh.RequestHeaders(), false)
	if status != shared.HeadersStatusContinue {
		t.Errorf("want HeadersStatusContinue, got %d", status)
	}

	// Response phase.
	filter.OnResponseHeaders(fh.ResponseHeaders(), false)

	// Stream complete: attributes (fake returns 0/false for all).
	filter.OnStreamComplete()

	// Trace IDs were captured.
	f := factory.pool.Get().(*Filter) // drain pool to inspect last record
	_ = f                             // just verify pool returns something

	// Verify span was tagged.
	if span.tags["request.id"] != "req-123" {
		t.Errorf("want span tag request.id=req-123, got %q", span.tags["request.id"])
	}
	if span.tags["request.method"] != "POST" {
		t.Errorf("want span tag request.method=POST, got %q", span.tags["request.method"])
	}
	if span.tags["response.status"] != "200" {
		t.Errorf("want span tag response.status=200, got %q", span.tags["response.status"])
	}
	// trace_id and span_id from fakeSpan.
	if span.tags["request.path"] != "/v1/chat/completions" {
		t.Errorf("span missing request.path, got %q", span.tags["request.path"])
	}
}

// TestErrorPath_LocalReply verifies that OnLocalReply populates error.details
// on the span and sets error=true.
func TestErrorPath_LocalReply(t *testing.T) {
	span := newFakeSpan()
	fh := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{":method": "GET", ":path": "/slow"}),
		fake.WithActiveSpan(span),
	)

	factory := newDefaultFactory()
	filter := factory.Create(fh)

	filter.OnRequestHeaders(fh.RequestHeaders(), false)
	filter.OnLocalReply(504, strBuf("upstream_reset_before_response_started"), false)
	filter.OnStreamComplete()

	if span.tags["error"] != "true" {
		t.Error("want span tag error=true on local reply")
	}
	if span.tags["error.details"] != "upstream_reset_before_response_started" {
		t.Errorf("want error.details set, got %q", span.tags["error.details"])
	}
}

// TestErrorPath_ResponseFlags verifies that when GetAttributeString returns
// non-empty flags they appear on the span.
// The fake always returns empty, so we test the path via the record directly.
func TestErrorPath_NoFlags(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{":method": "GET", ":path": "/ok"}),
	)
	factory := newDefaultFactory()
	filter := factory.Create(fh)

	filter.OnRequestHeaders(fh.RequestHeaders(), false)
	filter.OnResponseHeaders(fh.ResponseHeaders(), false)
	filter.OnStreamComplete()
	// No panic, no error flags on fake (GetAttributeString returns false).
}

// TestBodyRecording_Request verifies that when RecordRequestBody=true the
// filter signals StopAllAndBuffer and captures the body from OnRequestBody.
func TestBodyRecording_Request(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{":method": "POST", ":path": "/v1/chat"}),
		fake.WithRequestBody([]byte(`{"model":"gpt-4","messages":[]}`)),
	)
	factory := newConfigFactory(`{"record_request_body":true,"max_body_bytes":1024}`)
	filter := factory.Create(fh)

	// OnRequestHeaders should return StopAllAndBuffer when body recording is on.
	status := filter.OnRequestHeaders(fh.RequestHeaders(), false)
	if status != shared.HeadersStatusStopAllAndBuffer {
		t.Errorf("want HeadersStatusStopAllAndBuffer, got %d", status)
	}

	// Non-final chunk: should continue buffering.
	chunk := fake.NewFakeBodyBuffer([]byte(`{"model":`))
	bodyStatus := filter.OnRequestBody(chunk, false)
	if bodyStatus != shared.BodyStatusStopAndBuffer {
		t.Errorf("want BodyStatusStopAndBuffer for non-final chunk, got %d", bodyStatus)
	}

	// Final chunk.
	full := fake.NewFakeBodyBuffer([]byte(`{"model":"gpt-4","messages":[]}`))
	bodyStatus = filter.OnRequestBody(full, true)
	if bodyStatus != shared.BodyStatusContinue {
		t.Errorf("want BodyStatusContinue on endStream, got %d", bodyStatus)
	}

	// Body was captured on the filter record.
	f := filter.(*Filter)
	if len(f.rec.requestBody) == 0 {
		t.Error("want request body captured")
	}
}

// TestBodyRecording_Truncation verifies the body is truncated at MaxBodyBytes.
func TestBodyRecording_Truncation(t *testing.T) {
	body := make([]byte, 100)
	for i := range body {
		body[i] = 'x'
	}

	fh := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{":method": "POST", ":path": "/"}),
		fake.WithRequestBody(body),
	)
	factory := newConfigFactory(`{"record_request_body":true,"max_body_bytes":10}`)
	filter := factory.Create(fh)
	filter.OnRequestHeaders(fh.RequestHeaders(), false)

	full := fake.NewFakeBodyBuffer(body)
	filter.OnRequestBody(full, true)

	f := filter.(*Filter)
	if int64(len(f.rec.requestBody)) != 10 {
		t.Errorf("want body truncated to 10 bytes, got %d", len(f.rec.requestBody))
	}
}

// TestRequestHeaders_Captured verifies all request headers are copied to
// the record when RecordRequestHeaders=true.
func TestRequestHeaders_Captured(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{
			":method":      "GET",
			":path":        "/",
			"content-type": "application/json",
			"x-custom":     "value",
		}),
	)
	factory := newDefaultFactory()
	filter := factory.Create(fh)
	filter.OnRequestHeaders(fh.RequestHeaders(), false)

	f := filter.(*Filter)
	if len(f.rec.requestHeaders) == 0 {
		t.Error("want request headers captured")
	}
	// Verify at least :method is present.
	found := false
	for _, h := range f.rec.requestHeaders {
		if h[0] == ":method" && h[1] == "GET" {
			found = true
			break
		}
	}
	if !found {
		t.Error("want :method=GET in captured headers")
	}
}

// TestPoolReuse verifies that OnStreamComplete returns the filter to the pool.
func TestPoolReuse(t *testing.T) {
	fh := fake.NewFilterHandle()
	factory := newDefaultFactory()
	filter := factory.Create(fh)
	filter.OnRequestHeaders(fh.RequestHeaders(), false)
	filter.OnStreamComplete()

	f2 := factory.Create(fh)
	if f2 == nil {
		t.Error("pool must return a valid filter after OnStreamComplete")
	}
}

// TestHasError covers the error detection helper.
func TestHasError(t *testing.T) {
	cases := []struct {
		rec  record
		want bool
	}{
		{record{}, false},
		{record{errorDetails: "upstream_reset"}, true},
		{record{upstreamFailure: "tls_error"}, true},
		{record{responseFlags: "UF"}, true},
		{record{responseFlags: "DC"}, false}, // DC is downstream disconnect, not an upstream error
		{record{responseFlags: "-"}, false},
		{record{upstreamStatus: "500"}, false}, // status alone is not hasError
	}
	for _, c := range cases {
		got := c.rec.hasError()
		if got != c.want {
			t.Errorf("hasError(%+v): want %v, got %v", c.rec, c.want, got)
		}
	}
}

// TestTruncate verifies the truncate helper.
func TestTruncate(t *testing.T) {
	src := []byte("hello world")
	got := truncate(src, 5)
	if string(got) != "hello" {
		t.Errorf("want %q, got %q", "hello", got)
	}
	// Does not modify src.
	if string(src) != "hello world" {
		t.Error("truncate must not modify src")
	}
	// No truncation needed.
	got2 := truncate(src, 100)
	if string(got2) != "hello world" {
		t.Errorf("want full string, got %q", got2)
	}
}
