package hello_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dio/luwes/examples/hello"
	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
)

func TestHello_StampsResponseHeader(t *testing.T) {
	fac, err := hello.NewFactory(nil, nil)
	require.NoError(t, err)

	h := fake.NewFilterHandle(fake.WithHeaders(map[string]string{":path": "/v1/chat"}))
	filter := fac.Create(h)

	assert.Equal(t, shared.HeadersStatusContinue, filter.OnRequestHeaders(h.RequestHeaders(), false))
	assert.Equal(t, shared.HeadersStatusContinue, filter.OnResponseHeaders(h.ResponseHeaders(), false))
	assert.Equal(t, "from-luwes path=/v1/chat", h.ResponseHeaders().(*fake.FakeHeaderMap).GetString("x-hello"))
}

func TestHello_EmptyPath(t *testing.T) {
	fac, _ := hello.NewFactory(nil, nil)
	h := fake.NewFilterHandle(fake.WithHeaders(map[string]string{":path": ""}))
	filter := fac.Create(h)

	filter.OnRequestHeaders(h.RequestHeaders(), false)
	filter.OnResponseHeaders(h.ResponseHeaders(), false)
	assert.Equal(t, "from-luwes path=", h.ResponseHeaders().(*fake.FakeHeaderMap).GetString("x-hello"))
}
