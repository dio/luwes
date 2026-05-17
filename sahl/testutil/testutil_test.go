package sahltest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dio/luwes/sahl"
	sahltest "github.com/dio/luwes/sahl/testutil"
	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
)

func TestNewFilter_RequestAccept(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"x-api-key": "tok"}))
	f := sahltest.NewFilter("auth", func(w *sahl.Writer, r *sahl.Request) {
		key, ok := r.Header.Peek("x-api-key")
		require.True(t, ok)
		w.SetRequestHeader("x-user-id", key)
	}, fh)
	status := f.OnRequestHeaders(fh.RequestHeaders(), false)
	assert.Equal(t, shared.HeadersStatusContinue, status)
	assert.Equal(t, "tok", fh.RequestHeaders().(*fake.FakeHeaderMap).GetString("x-user-id"))
	f.OnStreamComplete()
	f.OnDestroy()
}

func TestNewFilter_RequestReject(t *testing.T) {
	fh := fake.NewFilterHandle()
	f := sahltest.NewFilter("auth", func(w *sahl.Writer, r *sahl.Request) {
		w.Send(401, `{"error":"missing key"}`)
	}, fh)
	f.OnRequestHeaders(fh.RequestHeaders(), false)
	require.Len(t, fh.LocalResponses, 1)
	assert.Equal(t, uint32(401), fh.LocalResponses[0].Status)
	f.OnStreamComplete()
	f.OnDestroy()
}

func TestNewFilterWithResponse_FullLifecycle(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithResponseHeaders(map[string]string{
			":status":      "200",
			"content-type": "application/json",
		}),
		fake.WithResponseBody([]byte(`{"ok":true}`)),
	)

	var (
		statusSeen  int
		contentSeen string
		bodySeen    string
		endSeen     bool
	)

	resp := func(w *sahl.Writer, chunk *sahl.ResponseChunk) {
		if chunk.Data == nil {
			statusSeen = chunk.StatusCode
			contentSeen = chunk.ContentType
			return
		}
		bodySeen = string(chunk.Data)
		endSeen = chunk.EndStream
	}

	f := sahltest.NewFilterWithResponse("obs", func(w *sahl.Writer, r *sahl.Request) {}, resp, fh)
	f.OnRequestHeaders(fh.RequestHeaders(), false)
	f.OnResponseHeaders(fh.ResponseHeaders(), false)
	f.OnResponseBody(fh.BufferedResponseBody(), true)
	f.OnStreamComplete()
	f.OnDestroy()

	assert.Equal(t, 200, statusSeen)
	assert.Equal(t, "application/json", contentSeen)
	assert.Equal(t, `{"ok":true}`, bodySeen)
	assert.True(t, endSeen)
}

func TestNewBodyAwareFilter(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithRequestBody([]byte("body-data")))
	var bodyRead string
	f := sahltest.NewBodyAwareFilter("body-filter", func(w *sahl.Writer, r *sahl.Request) {
		bodyRead = string(r.Body())
	}, fh)

	status := f.OnRequestHeaders(fh.RequestHeaders(), false)
	assert.Equal(t, shared.HeadersStatusStopAllAndBuffer, status)
	f.OnRequestBody(fh.BufferedRequestBody(), false)
	f.OnRequestBody(fh.BufferedRequestBody(), true)
	assert.Equal(t, "body-data", bodyRead)
	f.OnStreamComplete()
	f.OnDestroy()
}
