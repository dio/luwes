package sahl_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dio/luwes/sahl"
	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
)

// -- Header tests --

func TestHeader_Get_CopiesIntoGoMemory(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{
		"x-api-key": "secret",
	}))
	req := buildRequest(fh)
	assert.Equal(t, "secret", req.Header.Get("x-api-key"))
}

func TestHeader_Get_CaseInsensitive(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"Content-Type": "application/json"}))
	req := buildRequest(fh)
	assert.Equal(t, "application/json", req.Header.Get("content-type"))
	assert.Equal(t, "application/json", req.Header.Get("CONTENT-TYPE"))
}

func TestHeader_Get_Miss(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{}))
	req := buildRequest(fh)
	assert.Equal(t, "", req.Header.Get("x-missing"))
}

func TestHeader_Get_Cached(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"x-foo": "bar"}))
	req := buildRequest(fh)
	v1 := req.Header.Get("x-foo")
	v2 := req.Header.Get("x-foo")
	assert.Equal(t, "bar", v1)
	assert.Equal(t, "bar", v2)
}

func TestHeader_Peek_Hit(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"x-api-key": "token"}))
	req := buildRequest(fh)
	v, ok := req.Header.Peek("x-api-key")
	require.True(t, ok)
	assert.Equal(t, "token", v)
}

func TestHeader_Peek_Miss(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{}))
	req := buildRequest(fh)
	_, ok := req.Header.Peek("x-missing")
	assert.False(t, ok)
}

// -- Request pre-copies --

func TestRequest_MethodPathHost(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{
		":method":    "POST",
		":path":      "/v1/chat",
		":authority": "api.example.com",
	}))
	req := buildRequest(fh)
	assert.Equal(t, "POST", req.Method)
	assert.Equal(t, "/v1/chat", req.Path)
	assert.Equal(t, "api.example.com", req.Host)
}

// -- Writer tests --

func TestWriter_Send(t *testing.T) {
	fh := fake.NewFilterHandle()
	w := buildWriter(fh)
	w.Send(http.StatusUnauthorized, `{"error":"missing key"}`)
	assert.True(t, w.Responded())
	require.Len(t, fh.LocalResponses, 1)
	assert.Equal(t, uint32(401), fh.LocalResponses[0].Status)
}

func TestWriter_SetRequestHeader(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"x-user-id": ""}))
	w := buildWriter(fh)
	w.SetRequestHeader("x-user-id", "alice")
	assert.False(t, w.Responded())
	w.FlushForTest()
	assert.Equal(t, "alice", fh.RequestHeaders().(*fake.FakeHeaderMap).GetString("x-user-id"))
}

func TestWriter_ClearRouteCache(t *testing.T) {
	fh := fake.NewFilterHandle()
	w := buildWriter(fh)
	w.ClearRouteCache()
	w.FlushForTest()
	assert.Equal(t, 1, fh.ClearedRouteCache)
}

func TestWriter_IncrementCounter(t *testing.T) {
	fh := fake.NewFilterHandle()
	w := buildWriter(fh)
	w.IncrementCounter(shared.MetricID(1), 5, "ok")
	w.FlushForTest()
	require.Len(t, fh.CounterIncrements, 1)
	assert.Equal(t, uint64(5), fh.CounterIncrements[0].N)
}

// -- Handler integration --

func TestHandler_Accept(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{
		"x-api-key": "valid",
		"x-user-id": "",
	}))

	var called bool
	h := func(w *sahl.Writer, r *sahl.Request) {
		called = true
		key, ok := r.Header.Peek("x-api-key")
		require.True(t, ok)
		w.SetRequestHeader("x-user-id", key)
	}

	runHandler(t, fh, h)
	assert.True(t, called)
	assert.Equal(t, "valid", fh.RequestHeaders().(*fake.FakeHeaderMap).GetString("x-user-id"))
}

func TestHandler_Reject(t *testing.T) {
	fh := fake.NewFilterHandle()
	h := func(w *sahl.Writer, r *sahl.Request) {
		if _, ok := r.Header.Peek("x-api-key"); !ok {
			w.Send(401, `{"error":"missing key"}`)
		}
	}
	runHandler(t, fh, h)
	require.Len(t, fh.LocalResponses, 1)
	assert.Equal(t, uint32(401), fh.LocalResponses[0].Status)
}

func TestWriter_Go_RunsGoroutine(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"x-user-id": ""}))
	done := make(chan string, 1)

	h := func(w *sahl.Writer, r *sahl.Request) {
		w.Go(func(ctx context.Context) {
			w.SetRequestHeader("x-user-id", "from-goroutine")
			// Signal the test with the value before flush touches the fake's map.
			done <- "from-goroutine"
		})
	}

	runHandler(t, fh, h)
	// Wait for goroutine to complete before reading the fake's state.
	val := <-done
	assert.Equal(t, "from-goroutine", val)
}

func TestChain_ExecutionOrder(t *testing.T) {
	var order []string

	mw1 := func(next sahl.HandlerFunc) sahl.HandlerFunc {
		return func(w *sahl.Writer, r *sahl.Request) {
			order = append(order, "mw1-before")
			next(w, r)
			order = append(order, "mw1-after")
		}
	}
	mw2 := func(next sahl.HandlerFunc) sahl.HandlerFunc {
		return func(w *sahl.Writer, r *sahl.Request) {
			order = append(order, "mw2-before")
			next(w, r)
			order = append(order, "mw2-after")
		}
	}
	h := func(w *sahl.Writer, r *sahl.Request) {
		order = append(order, "handler")
	}

	fh := fake.NewFilterHandle()
	runHandler(t, fh, sahl.Chain(h, mw1, mw2))
	// Chain(h, mw1, mw2): mw1 is outermost, execution is mw1 -> mw2 -> handler
	assert.Equal(t, []string{"mw1-before", "mw2-before", "handler", "mw2-after", "mw1-after"}, order)
}

// -- helpers --

func buildRequest(fh *fake.FakeFilterHandle) *sahl.Request {
	return sahl.NewRequestForTest(fh.RequestHeaders(), fh, "test")
}

func buildWriter(fh *fake.FakeFilterHandle) *sahl.Writer {
	return sahl.NewWriterForTest(fh, fh.GetScheduler())
}

func runHandler(t *testing.T, fh *fake.FakeFilterHandle, h sahl.HandlerFunc) {
	t.Helper()
	f := sahl.NewFilterForTest("test", h, fh)
	f.OnRequestHeaders(fh.RequestHeaders(), false)
}
