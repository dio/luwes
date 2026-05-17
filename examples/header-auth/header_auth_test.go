package headerauth_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	headerauth "github.com/dio/luwes/examples/header-auth"
	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
)

func factory(t *testing.T) shared.HttpFilterFactory {
	t.Helper()
	f, err := headerauth.NewFactory(nil, nil)
	require.NoError(t, err)
	return f
}

func TestHeaderAuth_Accept(t *testing.T) {
	fac := factory(t)
	h := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"x-api-key": "tok-123"}))
	filter := fac.Create(h)

	status := filter.OnRequestHeaders(h.RequestHeaders(), false)
	assert.Equal(t, shared.HeadersStatusContinue, status)
	assert.Equal(t, "tok-123", h.RequestHeaders().(*fake.FakeHeaderMap).GetString("x-user-id"))
}

func TestHeaderAuth_Reject_MissingKey(t *testing.T) {
	fac := factory(t)
	h := fake.NewFilterHandle()
	filter := fac.Create(h)

	status := filter.OnRequestHeaders(h.RequestHeaders(), false)
	assert.Equal(t, shared.HeadersStatusStop, status)
	require.Len(t, h.LocalResponses, 1)
	assert.Equal(t, uint32(401), h.LocalResponses[0].Status)
}

func TestHeaderAuth_OnStreamComplete_ReturnsToPool(t *testing.T) {
	fac := factory(t)
	h := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"x-api-key": "tok"}))
	filter := fac.Create(h)
	filter.OnRequestHeaders(h.RequestHeaders(), false)
	assert.NotPanics(t, filter.OnStreamComplete)
}
