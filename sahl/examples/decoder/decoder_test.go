package decoder

import (
	"testing"
	"time"

	"github.com/dio/luwes/sahl"
	sahltest "github.com/dio/luwes/sahl/testutil"
	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resolveCluster --

func TestResolveCluster(t *testing.T) {
	cases := []struct {
		model   string
		cluster string
	}{
		{"gpt-4o", ClusterOpenAI},
		{"gpt-3.5-turbo", ClusterOpenAI},
		{"o1-preview", ClusterOpenAI},
		{"o3-mini", ClusterOpenAI},
		{"claude-3-5-sonnet-20241022", ClusterAnthropic},
		{"claude-haiku-3-5", ClusterAnthropic},
		{"unknown-model", ClusterDefault},
		{"", ClusterDefault},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.cluster, resolveCluster(tc.model), "model=%q", tc.model)
	}
}

// extractSSEUsage --

func TestExtractSSEUsage_OpenAI(t *testing.T) {
	tail := []byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":42}}\n")
	u := extractSSEUsage(nil, tail)
	assert.Equal(t, uint32(10), u.input)
	assert.Equal(t, uint32(42), u.output)
}

func TestExtractSSEUsage_Anthropic(t *testing.T) {
	head := []byte("event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":42}}}\n\n")
	tail := []byte("event: message_delta\ndata: {\"usage\":{\"output_tokens\":77}}\n\n")
	u := extractSSEUsage(head, tail)
	assert.Equal(t, uint32(42), u.input)
	assert.Equal(t, uint32(77), u.output)
}

func TestExtractSSEUsage_DONE(t *testing.T) {
	u := extractSSEUsage(nil, []byte("data: [DONE]\n"))
	assert.Equal(t, uint32(0), u.input)
	assert.Equal(t, uint32(0), u.output)
}

func TestExtractSSEUsage_Empty(t *testing.T) {
	u := extractSSEUsage(nil, nil)
	assert.Equal(t, uint32(0), u.input)
	assert.Equal(t, uint32(0), u.output)
}

func TestExtractSSEUsage_MalformedJSON(t *testing.T) {
	u := extractSSEUsage(nil, []byte("data: {not valid json}\n"))
	assert.Equal(t, uint32(0), u.input)
	assert.Equal(t, uint32(0), u.output)
}

// filter wiring --

type fakeDecoderConfigHandle struct{}

func (f *fakeDecoderConfigHandle) Log(_ shared.LogLevel, _ string, _ ...any) {}
func (f *fakeDecoderConfigHandle) DefineCounter(_ string, _ ...string) (shared.MetricID, shared.MetricsResult) {
	return 0, shared.MetricsSuccess
}
func (f *fakeDecoderConfigHandle) DefineGauge(_ string, _ ...string) (shared.MetricID, shared.MetricsResult) {
	return 0, shared.MetricsSuccess
}
func (f *fakeDecoderConfigHandle) DefineHistogram(_ string, _ ...string) (shared.MetricID, shared.MetricsResult) {
	return 0, shared.MetricsSuccess
}
func (f *fakeDecoderConfigHandle) HttpCallout(_ string, _ [][2]string, _ []byte, _ uint64, _ shared.HttpCalloutCallback) (shared.HttpCalloutInitResult, uint64) {
	return shared.HttpCalloutInitClusterNotFound, 0
}
func (f *fakeDecoderConfigHandle) StartHttpStream(_ string, _ [][2]string, _ []byte, _ bool, _ uint64, _ shared.HttpStreamCallback) (shared.HttpCalloutInitResult, uint64) {
	return shared.HttpCalloutInitClusterNotFound, 0
}
func (f *fakeDecoderConfigHandle) SendHttpStreamData(_ uint64, _ []byte, _ bool) bool  { return false }
func (f *fakeDecoderConfigHandle) SendHttpStreamTrailers(_ uint64, _ [][2]string) bool { return false }
func (f *fakeDecoderConfigHandle) ResetHttpStream(_ uint64)                            {}
func (f *fakeDecoderConfigHandle) GetScheduler() shared.Scheduler                      { return nil }

// buildDecoderFactory creates the decoder sahl filter factory.
func buildDecoderFactory(t *testing.T) shared.HttpFilterFactory {
	t.Helper()
	factories := sahl.Factories()
	def, ok := factories["decoder"]
	require.True(t, ok, "decoder not registered; init() must run")
	factory, err := def.Create(&fakeDecoderConfigHandle{}, nil)
	require.NoError(t, err)
	return factory
}

// runRequest runs a body-aware filter request through the full lifecycle.
// Returns the x-cluster header value set by the filter.
func runRequest(t *testing.T, body []byte) (string, shared.BodyStatus) {
	t.Helper()
	factory := buildDecoderFactory(t)

	headers := map[string]string{
		":method": "POST",
		":path":   "/v1/chat/completions",
	}

	fh := fake.NewFilterHandle(
		fake.WithHeaders(headers),
		fake.WithRequestBody(body),
	)

	f := factory.Create(fh)
	hs := f.OnRequestHeaders(fh.RequestHeaders(), false)
	assert.Equal(t, shared.HeadersStatusStopAllAndBuffer, hs, "body-aware: must stop and buffer at headers")

	bs := f.OnRequestBody(fh.ReceivedRequestBody(), true)

	f.OnStreamComplete()
	f.OnDestroy()

	cluster := ""
	for _, s := range fh.RequestHeaders().(*fake.FakeHeaderMap).Sets {
		if s.Key == "x-cluster" {
			cluster = s.Value
		}
	}
	return cluster, bs
}

func TestDecoderRequest_OpenAI(t *testing.T) {
	cluster, bs := runRequest(t, []byte(`{"model":"gpt-4o","messages":[]}`))
	assert.Equal(t, shared.BodyStatusContinue, bs)
	assert.Equal(t, ClusterOpenAI, cluster)
}

func TestDecoderRequest_Anthropic(t *testing.T) {
	cluster, _ := runRequest(t, []byte(`{"model":"claude-3-5-sonnet-20241022","messages":[]}`))
	assert.Equal(t, ClusterAnthropic, cluster)
}

func TestDecoderRequest_EmptyModel(t *testing.T) {
	cluster, bs := runRequest(t, []byte(`{}`))
	assert.Equal(t, shared.BodyStatusContinue, bs)
	assert.Equal(t, ClusterDefault, cluster)
}

func TestDecoderRequest_InvalidJSON(t *testing.T) {
	cluster, _ := runRequest(t, []byte(`not json`))
	assert.Equal(t, ClusterDefault, cluster)
}

func TestDecoderRequest_NoLocalResponse(t *testing.T) {
	factory := buildDecoderFactory(t)
	fh := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{":method": "POST"}),
		fake.WithRequestBody([]byte(`{"model":"gpt-4o"}`)),
	)
	f := factory.Create(fh)
	f.OnRequestHeaders(fh.RequestHeaders(), false)
	f.OnRequestBody(fh.ReceivedRequestBody(), true)
	f.OnStreamComplete()
	f.OnDestroy()

	assert.Empty(t, fh.LocalResponses, "decoder must not reject valid requests")
}

func TestDecoderRequest_PoolReuse(t *testing.T) {
	// Verify no state leaks across reused pool instances.
	models := []struct {
		model   string
		cluster string
	}{
		{"gpt-4o", ClusterOpenAI},
		{"claude-haiku-3-5", ClusterAnthropic},
		{"gpt-3.5-turbo", ClusterOpenAI},
		{"unknown", ClusterDefault},
	}
	for _, tc := range models {
		body := []byte(`{"model":"` + tc.model + `"}`)
		cluster, _ := runRequest(t, body)
		assert.Equal(t, tc.cluster, cluster, "model=%q", tc.model)
	}
}

// response phase --

// runResponse drives the full response-observer path for a given body and
// content-type, returning the FakeFilterHandle for counter assertions.
func runResponse(t *testing.T, contentType string, bodyChunks [][]byte) *fake.FakeFilterHandle {
	t.Helper()
	fh := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{
			":method": "POST",
			":path":   "/v1/chat/completions",
		}),
		fake.WithResponseHeaders(map[string]string{
			":status":      "200",
			"content-type": contentType,
		}),
	)
	f := sahltest.NewFilterWithResponse(
		"decoder",
		func(w *sahl.Writer, r *sahl.Request) {},
		DecoderResponseForTest,
		fh,
	)
	f.OnRequestHeaders(fh.RequestHeaders(), false)
	f.OnResponseHeaders(fh.ResponseHeaders(), false)
	for i, chunk := range bodyChunks {
		eos := i == len(bodyChunks)-1
		f.OnResponseBody(fake.NewFakeBodyBuffer(chunk), eos)
	}
	f.OnStreamComplete()
	f.OnDestroy()
	return fh
}

func TestDecoderResponse_SSE_OpenAI(t *testing.T) {
	body := []byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\ndata: [DONE]\n\n")
	fh := runResponse(t, "text/event-stream", [][]byte{body})

	var inN uint64
	for _, ci := range fh.CounterIncrements {
		if ci.N > 0 && !ci.Hist {
			inN += ci.N
		}
	}
	_ = inN
}

func TestDecoderResponse_SSE_MultiChunk(t *testing.T) {
	chunk1 := []byte("event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":12}}}\n\n")
	chunk2 := []byte("event: message_delta\ndata: {\"usage\":{\"output_tokens\":7}}\n\n")
	fh := runResponse(t, "text/event-stream", [][]byte{chunk1, chunk2})
	assert.NotEmpty(t, fh.CounterIncrements)
}

func TestDecoderResponse_JSON_NonStreaming(t *testing.T) {
	body := []byte(`{"usage":{"prompt_tokens":20,"completion_tokens":8}}`)
	fh := runResponse(t, "application/json", [][]byte{body})
	assert.NotEmpty(t, fh.CounterIncrements)
}

func TestDecoderResponse_JSON_MultiChunk(t *testing.T) {
	part1 := []byte(`{"usage":{"prompt_tokens":3,`)
	part2 := []byte(`"completion_tokens":9}}`)
	fh := runResponse(t, "application/json", [][]byte{part1, part2})
	assert.NotEmpty(t, fh.CounterIncrements)
}

func TestDecoderResponse_JSON_NoUsageField(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"hi"}}]}`)
	fh := runResponse(t, "application/json", [][]byte{body})
	// No usage field: no token counters incremented.
	var tokenIncrements int
	for _, ci := range fh.CounterIncrements {
		if !ci.Hist {
			tokenIncrements++
		}
	}
	assert.Zero(t, tokenIncrements)
}

func TestDecoderResponse_JSON_MalformedJSON(t *testing.T) {
	fh := runResponse(t, "application/json", [][]byte{[]byte(`not json`)})
	var tokenIncrements int
	for _, ci := range fh.CounterIncrements {
		if !ci.Hist {
			tokenIncrements++
		}
	}
	assert.Zero(t, tokenIncrements)
}

func TestDecoderResponse_JSON_EmptyBody(t *testing.T) {
	// tapJSONChunk with empty body: early return on len(jsonBuf)==0.
	fh := runResponse(t, "application/json", [][]byte{{}})
	assert.Empty(t, fh.CounterIncrements)
}

func TestDecoderResponse_SSE_NoUsage(t *testing.T) {
	body := []byte("data: {\"delta\":\"hi\"}\n\n")
	fh := runResponse(t, "text/event-stream", [][]byte{body})
	// No usage fields: no token counters.
	var tokenIncrements int
	for _, ci := range fh.CounterIncrements {
		if !ci.Hist {
			tokenIncrements++
		}
	}
	assert.Zero(t, tokenIncrements)
}

// TestClusterFromMeta confirms the fallback returns ClusterDefault.
func TestClusterFromMeta(t *testing.T) {
	// clusterFromMeta is a no-op that returns ClusterDefault.
	// Pass nil Writer the function ignores it.
	assert.Equal(t, ClusterDefault, clusterFromMeta(nil))
}

// TestSend400 confirms send400 sends a 400 with the right body.
func TestSend400(t *testing.T) {
	fh := fake.NewFilterHandle()
	w := sahl.NewWriterForTesting(fh)
	send400(w, "bad model")
	require.Len(t, fh.LocalResponses, 1)
	assert.Equal(t, uint32(400), fh.LocalResponses[0].Status)
	assert.Contains(t, string(fh.LocalResponses[0].Body), "bad model")
}

// TestEmitTTFT_ZeroElapsed exercises the ms==0 early-return branch in emitTTFT.
// When sentAt and firstChunk are identical (elapsed==0), no histogram is recorded.
func TestEmitTTFT_ZeroElapsed(t *testing.T) {
	fh := fake.NewFilterHandle()
	w := sahl.NewWriterForTesting(fh)
	now := time.Now()
	s := &respState{sentAt: now, firstChunk: now, cluster: ClusterDefault}
	emitTTFT(w, s)
	var histSeen bool
	for _, ci := range fh.CounterIncrements {
		if ci.Hist {
			histSeen = true
		}
	}
	assert.False(t, histSeen, "zero elapsed must not record histogram")
}

// TestEmitTTFT_NonZeroElapsed confirms the histogram is queued when elapsed > 0.
// Uses a response-observer path to flush mutations properly.
func TestEmitTTFT_NonZeroElapsed(t *testing.T) {
	// Build an SSE response where sentAt is artificially backdated so elapsed > 0.
	// We can't control sentAt externally, so drive through a 2ms sleep between
	// header call and body chunk delivery.
	fh := fake.NewFilterHandle(
		fake.WithResponseHeaders(map[string]string{
			":status":      "200",
			"content-type": "text/event-stream",
		}),
	)
	f := sahltest.NewFilterWithResponse("decoder",
		func(w *sahl.Writer, r *sahl.Request) {},
		DecoderResponseForTest,
		fh,
	)
	f.OnRequestHeaders(fh.RequestHeaders(), false)
	f.OnResponseHeaders(fh.ResponseHeaders(), false)
	time.Sleep(2 * time.Millisecond) // ensure elapsed > 0ms
	body := []byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\n")
	f.OnResponseBody(fake.NewFakeBodyBuffer(body), true)
	f.OnStreamComplete()

	var histSeen bool
	for _, ci := range fh.CounterIncrements {
		if ci.Hist {
			histSeen = true
			assert.GreaterOrEqual(t, ci.N, uint64(1))
		}
	}
	assert.True(t, histSeen, "expected TTFT histogram after non-zero elapsed")
}
