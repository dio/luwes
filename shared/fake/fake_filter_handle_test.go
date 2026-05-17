package fake

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dio/luwes/shared"
)

// -- constructor options --

func TestNewFilterHandle_Defaults(t *testing.T) {
	h := NewFilterHandle()
	assert.NotNil(t, h.RequestHeaders())
	assert.NotNil(t, h.ResponseHeaders())
	assert.NotNil(t, h.BufferedRequestBody())
	assert.NotNil(t, h.BufferedResponseBody())
	assert.Empty(t, h.LocalResponses)
	assert.Zero(t, h.ContinuedReq)
	assert.Zero(t, h.ContinuedResp)
	assert.Zero(t, h.ClearedRouteCache)
	assert.Empty(t, h.CounterIncrements)
}

func TestWithHeaders(t *testing.T) {
	h := NewFilterHandle(WithHeaders(map[string]string{"x-foo": "bar"}))
	assert.Equal(t, "bar", h.RequestHeaders().(*FakeHeaderMap).GetString("x-foo"))
}

func TestWithResponseHeaders(t *testing.T) {
	h := NewFilterHandle(WithResponseHeaders(map[string]string{"content-type": "application/json"}))
	assert.Equal(t, "application/json", h.ResponseHeaders().(*FakeHeaderMap).GetString("content-type"))
}

func TestWithRequestBody(t *testing.T) {
	h := NewFilterHandle(WithRequestBody([]byte("hello")))
	chunks := h.BufferedRequestBody().GetChunks()
	require.Len(t, chunks, 1)
	assert.Equal(t, "hello", chunks[0].ToUnsafeString())
	assert.True(t, h.ReceivedBufferedRequestBody())
}

func TestWithResponseBody(t *testing.T) {
	h := NewFilterHandle(WithResponseBody([]byte("world")))
	chunks := h.BufferedResponseBody().GetChunks()
	require.Len(t, chunks, 1)
	assert.Equal(t, "world", chunks[0].ToUnsafeString())
}

// -- header accessors --

func TestHeaderAccessors(t *testing.T) {
	h := NewFilterHandle(
		WithHeaders(map[string]string{"x-req": "req-val"}),
		WithResponseHeaders(map[string]string{"x-resp": "resp-val"}),
	)
	assert.Equal(t, "req-val", h.RequestHeaders().(*FakeHeaderMap).GetString("x-req"))
	assert.Equal(t, "resp-val", h.ResponseHeaders().(*FakeHeaderMap).GetString("x-resp"))
	// Trailers return empty maps, not nil.
	assert.NotNil(t, h.RequestTrailers())
	assert.NotNil(t, h.ResponseTrailers())
}

// -- body accessors --

func TestBodyAccessors(t *testing.T) {
	h := NewFilterHandle(WithRequestBody([]byte("req")), WithResponseBody([]byte("resp")))

	// Buffered and Received point to the same backing store.
	assert.Equal(t, h.BufferedRequestBody(), h.ReceivedRequestBody())
	assert.Equal(t, h.BufferedResponseBody(), h.ReceivedResponseBody())

	assert.True(t, h.ReceivedBufferedRequestBody())
	assert.False(t, h.ReceivedBufferedResponseBody())
}

func TestSetRequestBody(t *testing.T) {
	h := NewFilterHandle()
	h.SetRequestBody([]byte("replaced"))
	chunks := h.BufferedRequestBody().GetChunks()
	require.Len(t, chunks, 1)
	assert.Equal(t, "replaced", chunks[0].ToUnsafeString())
}

func TestSetReceivedBuffered(t *testing.T) {
	h := NewFilterHandle()
	h.SetReceivedBufferedRequestBody(true)
	h.SetReceivedBufferedResponseBody(true)
	assert.True(t, h.ReceivedBufferedRequestBody())
	assert.True(t, h.ReceivedBufferedResponseBody())
}

// -- flow control --

func TestContinueRequest(t *testing.T) {
	h := NewFilterHandle()
	h.ContinueRequest()
	h.ContinueRequest()
	assert.Equal(t, 2, h.ContinuedReq)
}

func TestContinueResponse(t *testing.T) {
	h := NewFilterHandle()
	h.ContinueResponse()
	assert.Equal(t, 1, h.ContinuedResp)
}

func TestClearRouteCache(t *testing.T) {
	h := NewFilterHandle()
	h.ClearRouteCache()
	h.ClearRouteCache()
	assert.Equal(t, 2, h.ClearedRouteCache)
}

func TestRefreshRouteCluster_NoOp(t *testing.T) {
	h := NewFilterHandle()
	h.RefreshRouteCluster() // no-op, just confirm no panic
}

// -- local response --

func TestSendLocalResponse(t *testing.T) {
	h := NewFilterHandle()
	h.SendLocalResponse(401, [][2]string{{"x-err", "auth"}}, []byte(`{"error":"denied"}`), "auth-filter")
	require.Len(t, h.LocalResponses, 1)
	r := h.LocalResponses[0]
	assert.Equal(t, uint32(401), r.Status)
	assert.Equal(t, `{"error":"denied"}`, string(r.Body))
	assert.Equal(t, "auth-filter", r.Detail)
	assert.Equal(t, [][2]string{{"x-err", "auth"}}, r.Headers)
}

func TestSendLocalResponse_Multiple(t *testing.T) {
	h := NewFilterHandle()
	h.SendLocalResponse(401, nil, nil, "first")
	h.SendLocalResponse(403, nil, nil, "second")
	assert.Len(t, h.LocalResponses, 2)
}

func TestSendResponse_NoOps(t *testing.T) {
	h := NewFilterHandle()
	h.SendResponseHeaders([][2]string{{"x-h", "v"}}, true)
	h.SendResponseData([]byte("data"), false)
	h.SendResponseTrailers([][2]string{{"x-t", "v"}})
}

// -- metadata --

func TestSetGetMetadataString(t *testing.T) {
	h := NewFilterHandle()
	h.SetMetadata("filter", "user-id", "alice")

	buf, ok := h.GetMetadataString(shared.MetadataSourceTypeDynamic, "filter", "user-id")
	require.True(t, ok)
	assert.Equal(t, "alice", buf.ToUnsafeString())
}

func TestGetMetadataString_TypeMismatch(t *testing.T) {
	h := NewFilterHandle()
	h.SetMetadata("ns", "key", float64(42)) // stored as float, not string
	_, ok := h.GetMetadataString(shared.MetadataSourceTypeDynamic, "ns", "key")
	assert.False(t, ok)
}

func TestGetMetadataString_Miss(t *testing.T) {
	h := NewFilterHandle()
	_, ok := h.GetMetadataString(shared.MetadataSourceTypeDynamic, "ns", "missing")
	assert.False(t, ok)
}

func TestSetGetMetadataNumber(t *testing.T) {
	h := NewFilterHandle()
	h.SetMetadata("ns", "score", float64(3.14))
	v, ok := h.GetMetadataNumber(shared.MetadataSourceTypeDynamic, "ns", "score")
	require.True(t, ok)
	assert.InDelta(t, 3.14, v, 0.001)
}

func TestGetMetadataNumber_TypeMismatch(t *testing.T) {
	h := NewFilterHandle()
	h.SetMetadata("ns", "key", "not-a-number")
	_, ok := h.GetMetadataNumber(shared.MetadataSourceTypeDynamic, "ns", "key")
	assert.False(t, ok)
}

func TestSetGetMetadataBool(t *testing.T) {
	h := NewFilterHandle()
	h.SetMetadata("ns", "flag", true)
	v, ok := h.GetMetadataBool(shared.MetadataSourceTypeDynamic, "ns", "flag")
	require.True(t, ok)
	assert.True(t, v)
}

func TestGetMetadataBool_TypeMismatch(t *testing.T) {
	h := NewFilterHandle()
	h.SetMetadata("ns", "key", "not-a-bool")
	_, ok := h.GetMetadataBool(shared.MetadataSourceTypeDynamic, "ns", "key")
	assert.False(t, ok)
}

func TestSetMetadata_MultipleNamespaces(t *testing.T) {
	h := NewFilterHandle()
	h.SetMetadata("a", "k", "v1")
	h.SetMetadata("b", "k", "v2")
	buf, ok := h.GetMetadataString(shared.MetadataSourceTypeDynamic, "a", "k")
	require.True(t, ok)
	assert.Equal(t, "v1", buf.ToUnsafeString())
}

func TestMetadataListOps_NoOps(t *testing.T) {
	h := NewFilterHandle()
	assert.False(t, h.AddMetadataListNumber("ns", "k", 1.0))
	assert.False(t, h.AddMetadataListString("ns", "k", "v"))
	assert.False(t, h.AddMetadataListBool("ns", "k", true))
	_, ok := h.GetMetadataListSize(shared.MetadataSourceTypeDynamic, "ns", "k")
	assert.False(t, ok)
	_, ok = h.GetMetadataListNumber(shared.MetadataSourceTypeDynamic, "ns", "k", 0)
	assert.False(t, ok)
	_, ok = h.GetMetadataListString(shared.MetadataSourceTypeDynamic, "ns", "k", 0)
	assert.False(t, ok)
	_, ok = h.GetMetadataListBool(shared.MetadataSourceTypeDynamic, "ns", "k", 0)
	assert.False(t, ok)
}

func TestGetMetadataKeysNamespaces_NoOps(t *testing.T) {
	h := NewFilterHandle()
	assert.Nil(t, h.GetMetadataKeys(shared.MetadataSourceTypeDynamic, "ns"))
	assert.Nil(t, h.GetMetadataNamespaces(shared.MetadataSourceTypeDynamic))
}

// -- attributes --

func TestAttributes_NoOps(t *testing.T) {
	h := NewFilterHandle()
	_, ok := h.GetAttributeString(0)
	assert.False(t, ok)
	_, ok = h.GetAttributeNumber(0)
	assert.False(t, ok)
	_, ok = h.GetAttributeBool(0)
	assert.False(t, ok)
}

// -- filter state --

func TestFilterState_GetMiss(t *testing.T) {
	h := NewFilterHandle()
	_, ok := h.GetFilterState("missing")
	assert.False(t, ok)
}

func TestFilterState_SetNoOp(t *testing.T) {
	h := NewFilterHandle()
	h.SetFilterState("key", []byte("val")) // no-op; Get still misses
	_, ok := h.GetFilterState("key")
	assert.False(t, ok)
}

func TestFilterState_Typed(t *testing.T) {
	h := NewFilterHandle()
	ok := h.SetFilterStateTyped("key", []byte("val"))
	assert.False(t, ok)
	_, ok = h.GetFilterStateTyped("key")
	assert.False(t, ok)
}

// -- cross-phase data --

func TestData_NoOps(t *testing.T) {
	h := NewFilterHandle()
	assert.Nil(t, h.GetData("key"))
	h.SetData("key", "val")
	assert.Nil(t, h.GetData("key")) // still nil; fake does not store
	assert.Nil(t, h.GetMostSpecificConfig())
}

// -- logging --

func TestLogging(t *testing.T) {
	h := NewFilterHandle()
	h.Log(shared.LogLevelInfo, "test %s", "msg") // no-op, no panic
	assert.False(t, h.LogEnabled(shared.LogLevelTrace))
	assert.False(t, h.LogEnabled(shared.LogLevelInfo))
}

// -- scheduler --

func TestGetScheduler_SynchronousSchedule(t *testing.T) {
	h := NewFilterHandle()
	sched := h.GetScheduler()
	require.NotNil(t, sched)
	var ran bool
	sched.Schedule(func() { ran = true })
	assert.True(t, ran) // fakeScheduler runs synchronously
}

// -- HTTP callout / stream --

func TestHttpCallout_ClusterNotFound(t *testing.T) {
	h := NewFilterHandle()
	result, id := h.HttpCallout("nonexistent", nil, nil, 0, nil)
	assert.Equal(t, shared.HttpCalloutInitClusterNotFound, result)
	assert.Zero(t, id)
}

func TestHttpStream_NoOps(t *testing.T) {
	h := NewFilterHandle()
	result, id := h.StartHttpStream("cluster", nil, nil, false, 0, nil)
	assert.Equal(t, shared.HttpCalloutInitClusterNotFound, result)
	assert.Zero(t, id)
	assert.False(t, h.SendHttpStreamData(0, nil, false))
	assert.False(t, h.SendHttpStreamTrailers(0, nil))
	h.ResetHttpStream(0)
}

// -- watermarks --

func TestWatermarks_NoOps(t *testing.T) {
	h := NewFilterHandle()
	h.SetDownstreamWatermarkCallbacks(nil)
	h.ClearDownstreamWatermarkCallbacks()
}

// -- metrics --

func TestRecordHistogramValue(t *testing.T) {
	h := NewFilterHandle()
	res := h.RecordHistogramValue(shared.MetricID(1), 42, "ok")
	assert.Equal(t, shared.MetricsSuccess, res)
	require.Len(t, h.CounterIncrements, 1)
	ci := h.CounterIncrements[0]
	assert.Equal(t, shared.MetricID(1), ci.ID)
	assert.Equal(t, uint64(42), ci.N)
	assert.Equal(t, []string{"ok"}, ci.Tags)
	assert.True(t, ci.Hist)
}

func TestIncrementCounterValue(t *testing.T) {
	h := NewFilterHandle()
	res := h.IncrementCounterValue(shared.MetricID(2), 7, "tag1", "tag2")
	assert.Equal(t, shared.MetricsSuccess, res)
	require.Len(t, h.CounterIncrements, 1)
	ci := h.CounterIncrements[0]
	assert.Equal(t, shared.MetricID(2), ci.ID)
	assert.Equal(t, uint64(7), ci.N)
	assert.Equal(t, []string{"tag1", "tag2"}, ci.Tags)
	assert.False(t, ci.Hist)
}

func TestGaugeMetrics_NoOp(t *testing.T) {
	h := NewFilterHandle()
	assert.Equal(t, shared.MetricsSuccess, h.SetGaugeValue(0, 1))
	assert.Equal(t, shared.MetricsSuccess, h.IncrementGaugeValue(0, 1))
	assert.Equal(t, shared.MetricsSuccess, h.DecrementGaugeValue(0, 1))
}

// -- misc --

func TestMisc_NoOps(t *testing.T) {
	h := NewFilterHandle()
	h.AddCustomFlag("flag")
	assert.Zero(t, h.GetWorkerIndex())
	assert.Zero(t, h.GetBufferLimit())
	h.SetBufferLimit(1024)
	assert.Nil(t, h.GetActiveSpan())
	_, ok := h.GetClusterName()
	assert.False(t, ok)
	_, ok = h.GetClusterHostCounts(0)
	assert.False(t, ok)
	assert.False(t, h.SetUpstreamOverrideHost("host", true))
	h.ResetStream(0, "reason")
	h.SendGoAwayAndClose(true)
	assert.False(t, h.RecreateStream(nil))
}

func TestSocketOptions_NoOps(t *testing.T) {
	h := NewFilterHandle()
	assert.False(t, h.SetSocketOptionInt(0, 0, 0, 0, 0))
	assert.False(t, h.SetSocketOptionBytes(0, 0, 0, 0, nil))
	_, ok := h.GetSocketOptionInt(0, 0, 0, 0)
	assert.False(t, ok)
	_, ok = h.GetSocketOptionBytes(0, 0, 0, 0)
	assert.False(t, ok)
}

// -- bench_handle --

func TestNewBenchFilterHandle(t *testing.T) {
	bh := NewBenchFilterHandle()
	require.NotNil(t, bh)
	hm := bh.RequestHeaders()
	require.NotNil(t, hm)
}

func TestSilentHeaderMap_Set(t *testing.T) {
	bh := NewBenchFilterHandle()
	hm := bh.RequestHeaders().(*SilentHeaderMap)
	// Pre-populate so Set is an overwrite (no alloc path).
	hm.Set("x-user-id", "before")
	hm.Set("x-user-id", "after")
	// SilentHeaderMap discards the value; we just confirm no panic.
}

func TestSilentHeaderMap_Add(t *testing.T) {
	bh := NewBenchFilterHandle()
	hm := bh.RequestHeaders().(*SilentHeaderMap)
	hm.Add("x-h", "v") // no-op, no panic
}

func TestGetString_Miss(t *testing.T) {
	hm := NewFakeHeaderMap(nil)
	assert.Equal(t, "", hm.GetString("x-missing"))
}

func TestAsciiToLower(t *testing.T) {
	cases := [][2]string{
		{"X-Api-Key", "x-api-key"},
		{"CONTENT-TYPE", "content-type"},
		{"already-lower", "already-lower"},
		{"Mixed123", "mixed123"},
	}
	for _, c := range cases {
		assert.Equal(t, c[1], asciiToLower(c[0]))
	}
}
