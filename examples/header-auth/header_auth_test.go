package headerauth_test

import (
	"testing"

	headerauth "github.com/dio/luwes/examples/header-auth"
	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
)

func factory(t *testing.T) shared.HttpFilterFactory {
	t.Helper()
	f, err := headerauth.NewFactory(nil, nil)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	return f
}

func TestHeaderAuth_Accept(t *testing.T) {
	fac := factory(t)
	h := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{"x-api-key": "tok-123"}),
	)
	filter := fac.Create(h)

	status := filter.OnRequestHeaders(h.RequestHeaders(), false)
	if status != shared.HeadersStatusContinue {
		t.Fatalf("got %v, want HeadersStatusContinue", status)
	}
	// x-user-id should be set to the key value.
	if got := h.RequestHeaders().(*fake.FakeHeaderMap).GetString("x-user-id"); got != "tok-123" {
		t.Fatalf("x-user-id = %q, want %q", got, "tok-123")
	}
}

func TestHeaderAuth_Reject_MissingKey(t *testing.T) {
	fac := factory(t)
	h := fake.NewFilterHandle()
	filter := fac.Create(h)

	status := filter.OnRequestHeaders(h.RequestHeaders(), false)
	if status != shared.HeadersStatusStop {
		t.Fatalf("got %v, want HeadersStatusStop", status)
	}
	if len(h.LocalResponses) == 0 {
		t.Fatal("expected local response to be sent")
	}
	if h.LocalResponses[0].Status != 401 {
		t.Fatalf("response code = %d, want 401", h.LocalResponses[0].Status)
	}
}

func TestHeaderAuth_OnStreamComplete_ReturnsToPool(t *testing.T) {
	fac := factory(t)
	h := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{"x-api-key": "tok"}),
	)
	filter := fac.Create(h)
	filter.OnRequestHeaders(h.RequestHeaders(), false)
	// OnStreamComplete should not panic and should nil the handle field.
	filter.OnStreamComplete()
}
