package hello_test

import (
	"testing"

	"github.com/dio/luwes/examples/hello"
	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
)

func TestHello_StampsResponseHeader(t *testing.T) {
	fac, err := hello.NewFactory(nil, nil)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}

	h := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{":path": "/v1/chat"}),
	)
	filter := fac.Create(h)

	if status := filter.OnRequestHeaders(h.RequestHeaders(), false); status != shared.HeadersStatusContinue {
		t.Fatalf("OnRequestHeaders = %v, want Continue", status)
	}
	if status := filter.OnResponseHeaders(h.ResponseHeaders(), false); status != shared.HeadersStatusContinue {
		t.Fatalf("OnResponseHeaders = %v, want Continue", status)
	}

	got := h.ResponseHeaders().(*fake.FakeHeaderMap).GetString("x-hello")
	if got != "from-luwes path=/v1/chat" {
		t.Fatalf("x-hello = %q, want %q", got, "from-luwes path=/v1/chat")
	}
}

func TestHello_EmptyPath(t *testing.T) {
	fac, _ := hello.NewFactory(nil, nil)
	h := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{":path": ""}),
	)
	filter := fac.Create(h)
	filter.OnRequestHeaders(h.RequestHeaders(), false)
	filter.OnResponseHeaders(h.ResponseHeaders(), false)

	got := h.ResponseHeaders().(*fake.FakeHeaderMap).GetString("x-hello")
	if got != "from-luwes path=" {
		t.Fatalf("x-hello = %q, want %q", got, "from-luwes path=")
	}
}
