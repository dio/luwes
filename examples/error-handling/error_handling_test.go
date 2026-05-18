package errorhandling

import (
	"testing"

	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
)

// strBuf wraps a Go string as an UnsafeEnvoyBuffer for use in tests.
func strBuf(s string) shared.UnsafeEnvoyBuffer {
	if s == "" {
		return shared.UnsafeEnvoyBuffer{}
	}
	b := []byte(s)
	return shared.UnsafeEnvoyBuffer{Ptr: &b[0], Len: uint64(len(b))}
}

// makeRespHeaders builds a [][2]UnsafeEnvoyBuffer from flat key/value pairs.
func makeRespHeaders(pairs ...string) [][2]shared.UnsafeEnvoyBuffer {
	out := make([][2]shared.UnsafeEnvoyBuffer, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, [2]shared.UnsafeEnvoyBuffer{strBuf(pairs[i]), strBuf(pairs[i+1])})
	}
	return out
}

// newFactory creates a Factory with a nil config handle (no metrics).
func newFactory() *Factory {
	f, _ := NewFactory(nil, nil)
	return f.(*Factory)
}

// lastReply returns the most recent local response recorded by the fake.
func lastReply(fh *fake.FakeFilterHandle) (code uint32, detail string, ok bool) {
	if len(fh.LocalResponses) == 0 {
		return 0, "", false
	}
	r := fh.LocalResponses[len(fh.LocalResponses)-1]
	return r.Status, r.Detail, true
}

// TestCalloutInitFailure verifies error path 1:
// cluster not found -> 503, ContinueRequest never called.
func TestCalloutInitFailure(t *testing.T) {
	// Default fake: HttpCallout returns HttpCalloutInitClusterNotFound.
	fh := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{":path": "/api/test"}),
	)
	factory := newFactory()
	filter := factory.Create(fh)

	status := filter.OnRequestHeaders(fh.RequestHeaders(), false)

	if status != shared.HeadersStatusStop {
		t.Errorf("want HeadersStatusStop, got %d", status)
	}
	code, detail, ok := lastReply(fh)
	if !ok {
		t.Fatal("want SendLocalResponse called on init failure")
	}
	if code != 503 {
		t.Errorf("want 503, got %d", code)
	}
	if detail != "callout_init_fail" {
		t.Errorf("want detail=callout_init_fail, got %q", detail)
	}
	if fh.ContinuedReq != 0 {
		t.Error("ContinueRequest must not be called on init failure")
	}
}

// TestCalloutNetworkFailure verifies error path 2:
// callout init succeeds but upstream resets -> 502.
func TestCalloutNetworkFailure(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{":path": "/api/reset"}),
		fake.WithHTTPCalloutFn(func(
			_ string, _ [][2]string, _ []byte, _ uint64,
			cb shared.HttpCalloutCallback,
		) (shared.HttpCalloutInitResult, uint64) {
			cb.OnHttpCalloutDone(1, shared.HttpCalloutReset, nil, nil)
			return shared.HttpCalloutInitSuccess, 1
		}),
	)
	factory := newFactory()
	filter := factory.Create(fh)

	filter.OnRequestHeaders(fh.RequestHeaders(), false)

	code, detail, ok := lastReply(fh)
	if !ok {
		t.Fatal("want SendLocalResponse called on network failure")
	}
	if code != 502 {
		t.Errorf("want 502 on callout reset, got %d", code)
	}
	if detail != "callout_net_fail" {
		t.Errorf("want detail=callout_net_fail, got %q", detail)
	}
	if fh.ContinuedReq != 0 {
		t.Error("ContinueRequest must not be called on network failure")
	}
}

// TestCalloutUpstreamError_401 verifies error path 3a:
// auth returns 401 -> filter sends 401 downstream.
func TestCalloutUpstreamError_401(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{":path": "/api/secret"}),
		fake.WithHTTPCalloutFn(func(
			_ string, _ [][2]string, _ []byte, _ uint64,
			cb shared.HttpCalloutCallback,
		) (shared.HttpCalloutInitResult, uint64) {
			cb.OnHttpCalloutDone(1, shared.HttpCalloutSuccess,
				makeRespHeaders(":status", "401"), nil)
			return shared.HttpCalloutInitSuccess, 1
		}),
	)
	factory := newFactory()
	filter := factory.Create(fh)

	filter.OnRequestHeaders(fh.RequestHeaders(), false)

	code, _, ok := lastReply(fh)
	if !ok {
		t.Fatal("want SendLocalResponse for 401")
	}
	if code != 401 {
		t.Errorf("want 401 for upstream 401, got %d", code)
	}
}

// TestCalloutUpstreamError_5xx verifies error path 3b:
// auth returns 500 -> filter sends 403 (generic denial, hide internal error).
func TestCalloutUpstreamError_5xx(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{":path": "/api/internal"}),
		fake.WithHTTPCalloutFn(func(
			_ string, _ [][2]string, _ []byte, _ uint64,
			cb shared.HttpCalloutCallback,
		) (shared.HttpCalloutInitResult, uint64) {
			cb.OnHttpCalloutDone(1, shared.HttpCalloutSuccess,
				makeRespHeaders(":status", "500"), nil)
			return shared.HttpCalloutInitSuccess, 1
		}),
	)
	factory := newFactory()
	filter := factory.Create(fh)

	filter.OnRequestHeaders(fh.RequestHeaders(), false)

	code, _, ok := lastReply(fh)
	if !ok {
		t.Fatal("want SendLocalResponse for 5xx")
	}
	if code != 403 {
		t.Errorf("want 403 for upstream 5xx, got %d", code)
	}
}

// TestCalloutSuccess verifies the happy path:
// auth returns 200 with x-auth-user -> header injected, ContinueRequest called.
func TestCalloutSuccess(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{":path": "/api/ok"}),
		fake.WithHTTPCalloutFn(func(
			_ string, _ [][2]string, _ []byte, _ uint64,
			cb shared.HttpCalloutCallback,
		) (shared.HttpCalloutInitResult, uint64) {
			cb.OnHttpCalloutDone(1, shared.HttpCalloutSuccess,
				makeRespHeaders(":status", "200", "x-auth-user", "alice"), nil)
			return shared.HttpCalloutInitSuccess, 1
		}),
	)
	factory := newFactory()
	filter := factory.Create(fh)

	filter.OnRequestHeaders(fh.RequestHeaders(), false)

	if len(fh.LocalResponses) != 0 {
		t.Errorf("want no SendLocalResponse on success, got %d calls", len(fh.LocalResponses))
	}
	if fh.ContinuedReq != 1 {
		t.Errorf("want ContinueRequest called once, got %d", fh.ContinuedReq)
	}
	got := fh.RequestHeaders().(*fake.FakeHeaderMap).GetString("x-auth-user")
	if got != "alice" {
		t.Errorf("want x-auth-user=alice, got %q", got)
	}
}

// TestOnLocalReply verifies error path 4:
// Envoy-generated local reply -> diagnostic header set on response.
func TestOnLocalReply(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{":path": "/api/timeout"}),
	)
	factory := newFactory()
	filter := factory.Create(fh)

	lrStatus := filter.OnLocalReply(504, strBuf("upstream_reset_before_response_started"), false)

	if lrStatus != shared.LocalReplyStatusContinue {
		t.Errorf("want LocalReplyStatusContinue, got %d", lrStatus)
	}
	got := fh.ResponseHeaders().(*fake.FakeHeaderMap).GetString("x-error-details")
	if got != "upstream_reset_before_response_started" {
		t.Errorf("want x-error-details set, got %q", got)
	}
}

// TestOnLocalReply_ResetImminent verifies the filter skips setting the response
// header when reset is imminent (no headers will be sent).
func TestOnLocalReply_ResetImminent(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{":path": "/api/reset"}),
	)
	factory := newFactory()
	filter := factory.Create(fh)

	filter.OnLocalReply(503, strBuf("some_detail"), true)

	got := fh.ResponseHeaders().(*fake.FakeHeaderMap).GetString("x-error-details")
	if got != "" {
		t.Errorf("want no x-error-details when reset imminent, got %q", got)
	}
}

// TestOnStreamComplete verifies error path 5:
// fake GetAttributeString returns empty, filter completes without panic,
// instance is returned to pool.
func TestOnStreamComplete(t *testing.T) {
	fh := fake.NewFilterHandle()
	factory := newFactory()
	filter := factory.Create(fh)

	// Should not panic even with empty flags.
	filter.OnStreamComplete()

	// After OnStreamComplete the filter is returned to the pool.
	f2 := factory.Create(fh)
	if f2 == nil {
		t.Error("pool must return a valid filter after OnStreamComplete")
	}
}

// TestItoa covers the zero-alloc integer formatter.
func TestItoa(t *testing.T) {
	cases := []struct {
		n    uint32
		want string
	}{
		{0, "0"},
		{1, "1"},
		{200, "200"},
		{503, "503"},
		{4294967295, "4294967295"},
	}
	for _, c := range cases {
		got := itoa(c.n)
		if got != c.want {
			t.Errorf("itoa(%d): want %q, got %q", c.n, c.want, got)
		}
	}
}
