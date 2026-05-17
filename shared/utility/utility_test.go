package utility_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dio/luwes/shared/fake"
	"github.com/dio/luwes/shared/utility"
)

func TestReadWholeRequestBody_Buffered(t *testing.T) {
	h := fake.NewFilterHandle(fake.WithRequestBody([]byte("hello world")))
	h.SetReceivedBufferedRequestBody(true)

	got := utility.ReadWholeRequestBody(h)
	require.Equal(t, "hello world", string(got))
}

func TestReadWholeRequestBody_Empty(t *testing.T) {
	h := fake.NewFilterHandle()
	h.SetReceivedBufferedRequestBody(true)

	assert.Empty(t, utility.ReadWholeRequestBody(h))
}

func TestReadWholeResponseBody_Buffered(t *testing.T) {
	h := fake.NewFilterHandle(fake.WithResponseBody([]byte("response body")))
	h.SetReceivedBufferedResponseBody(true)

	require.Equal(t, "response body", string(utility.ReadWholeResponseBody(h)))
}

func TestReadWholeResponseBody_Empty(t *testing.T) {
	h := fake.NewFilterHandle()
	h.SetReceivedBufferedResponseBody(true)

	assert.Empty(t, utility.ReadWholeResponseBody(h))
}
