package utility_test

import (
	"testing"

	"github.com/dio/luwes/shared/fake"
	"github.com/dio/luwes/shared/utility"
)

func TestReadWholeRequestBody_Buffered(t *testing.T) {
	h := fake.NewFilterHandle(
		fake.WithRequestBody([]byte("hello world")),
	)
	// buffered=true: only buffered body is read, not received.
	h.SetReceivedBufferedRequestBody(true)

	got := utility.ReadWholeRequestBody(h)
	if string(got) != "hello world" {
		t.Fatalf("got %q, want %q", got, "hello world")
	}
}

func TestReadWholeRequestBody_Empty(t *testing.T) {
	h := fake.NewFilterHandle()
	h.SetReceivedBufferedRequestBody(true)

	got := utility.ReadWholeRequestBody(h)
	if len(got) != 0 {
		t.Fatalf("expected empty body, got %q", got)
	}
}

func TestReadWholeResponseBody_Buffered(t *testing.T) {
	h := fake.NewFilterHandle(
		fake.WithResponseBody([]byte("response body")),
	)
	h.SetReceivedBufferedResponseBody(true)

	got := utility.ReadWholeResponseBody(h)
	if string(got) != "response body" {
		t.Fatalf("got %q, want %q", got, "response body")
	}
}

func TestReadWholeResponseBody_Empty(t *testing.T) {
	h := fake.NewFilterHandle()
	h.SetReceivedBufferedResponseBody(true)

	got := utility.ReadWholeResponseBody(h)
	if len(got) != 0 {
		t.Fatalf("expected empty body, got %q", got)
	}
}
