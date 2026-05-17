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
	// WithRequestBody sets bufferedReq=true; reads from reqBody only.
	got := utility.ReadWholeRequestBody(h)
	require.Equal(t, "hello world", string(got))
}

func TestReadWholeRequestBody_Unbuffered(t *testing.T) {
	// isBuffered=false: utility must combine buffered chunks + latest received chunk.
	h := fake.NewFilterHandle(
		fake.WithRequestBody([]byte("buffered ")),
		fake.WithReceivedRequestBody([]byte("received")),
	)
	// bufferedReq defaults to false when WithReceivedRequestBody is used without WithRequestBody
	// overriding it. But WithRequestBody sets bufferedReq=true, so we clear it.
	h.SetReceivedBufferedRequestBody(false)

	got := utility.ReadWholeRequestBody(h)
	require.Equal(t, "buffered received", string(got))
}

func TestReadWholeRequestBody_UnbufferedReceivedOnly(t *testing.T) {
	// No buffered body; received body only (common on first chunk).
	h := fake.NewFilterHandle(fake.WithReceivedRequestBody([]byte("first chunk")))
	// bufferedReq is false by default when only WithReceivedRequestBody is used.
	got := utility.ReadWholeRequestBody(h)
	require.Equal(t, "first chunk", string(got))
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

func TestReadWholeResponseBody_Unbuffered(t *testing.T) {
	h := fake.NewFilterHandle(
		fake.WithResponseBody([]byte("first ")),
		fake.WithReceivedResponseBody([]byte("second")),
	)
	// bufferedResp false by default.
	got := utility.ReadWholeResponseBody(h)
	require.Equal(t, "first second", string(got))
}

func TestReadWholeResponseBody_Empty(t *testing.T) {
	h := fake.NewFilterHandle()
	h.SetReceivedBufferedResponseBody(true)
	assert.Empty(t, utility.ReadWholeResponseBody(h))
}
