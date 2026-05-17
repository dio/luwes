package decoder

import (
	"testing"

	"github.com/dio/luwes/sahl"
	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -- resolveCluster --

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

// -- extractSSEUsage --

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

// -- filter wiring --

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
