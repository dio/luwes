package observability_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dio/luwes/examples/observability"
	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
)

// -- factory helpers --

func newFactory(t *testing.T) shared.HttpFilterFactory {
	t.Helper()
	f, err := observability.NewFactory(&fakeConfigHandle{}, nil)
	require.NoError(t, err)
	return f
}

// -- NewFactory --

func TestNewFactory_DefinesMetrics(t *testing.T) {
	cfg := &fakeConfigHandle{}
	f, err := observability.NewFactory(cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, f)
	assert.Equal(t, 2, cfg.defineCount) // counter + histogram
}

func TestFactory_OnDestroy_NoOp(t *testing.T) {
	fac := newFactory(t)
	assert.NotPanics(t, fac.OnDestroy)
}

func TestFactory_Create_ReturnsFilter(t *testing.T) {
	fac := newFactory(t)
	h := fake.NewFilterHandle()
	filter := fac.Create(h)
	require.NotNil(t, filter)
}

// -- OnRequestHeaders --

func TestOnRequestHeaders_SetsMetadataAndCounter(t *testing.T) {
	fac := newFactory(t)
	h := fake.NewFilterHandle(fake.WithHeaders(map[string]string{
		":method": "GET",
		":path":   "/v1/models",
	}))
	filter := fac.Create(h)

	status := filter.OnRequestHeaders(h.RequestHeaders(), false)
	assert.Equal(t, shared.HeadersStatusContinue, status)

	// Counter incremented once.
	require.Len(t, h.CounterIncrements, 1)
	assert.Equal(t, uint64(1), h.CounterIncrements[0].N)

	// Metadata set for method and path.
	methodBuf, ok := h.GetMetadataString(shared.MetadataSourceTypeDynamic, "luwes", "method")
	require.True(t, ok)
	assert.Equal(t, "GET", methodBuf.ToUnsafeString())

	pathBuf, ok := h.GetMetadataString(shared.MetadataSourceTypeDynamic, "luwes", "path")
	require.True(t, ok)
	assert.Equal(t, "/v1/models", pathBuf.ToUnsafeString())
}

func TestOnRequestHeaders_MissingPseudoHeaders(t *testing.T) {
	// No :method or :path: GetOne returns zero UnsafeEnvoyBuffer, ToString returns "".
	fac := newFactory(t)
	h := fake.NewFilterHandle()
	filter := fac.Create(h)
	status := filter.OnRequestHeaders(h.RequestHeaders(), false)
	assert.Equal(t, shared.HeadersStatusContinue, status)
}

func TestOnRequestHeaders_WithActiveSpan(t *testing.T) {
	// LogEnabled=true + active span: exercises SetTag, SpawnChild, Finish.
	fac := newFactory(t)
	span := &fakeSpan{}
	h := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{":method": "POST", ":path": "/v1/chat"}),
		fake.WithActiveSpan(span),
		fake.WithLogEnabled(true),
	)
	filter := fac.Create(h)
	status := filter.OnRequestHeaders(h.RequestHeaders(), false)
	assert.Equal(t, shared.HeadersStatusContinue, status)
	assert.True(t, span.tagSet)
	assert.True(t, span.childSpawned)
	assert.True(t, span.child.finished)
}

func TestOnRequestHeaders_SpanWithNilChild(t *testing.T) {
	// SpawnChild returns nil: the nil guard is exercised.
	fac := newFactory(t)
	span := &fakeSpan{nilChild: true}
	h := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{":method": "GET", ":path": "/"}),
		fake.WithActiveSpan(span),
	)
	filter := fac.Create(h)
	// Must not panic when child is nil.
	assert.NotPanics(t, func() {
		filter.OnRequestHeaders(h.RequestHeaders(), false)
	})
}

func TestOnRequestHeaders_LogEnabled_EmitsLog(t *testing.T) {
	// LogEnabled=true: the log call path is exercised.
	fac := newFactory(t)
	h := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{":method": "PUT", ":path": "/foo"}),
		fake.WithLogEnabled(true),
	)
	filter := fac.Create(h)
	assert.NotPanics(t, func() {
		filter.OnRequestHeaders(h.RequestHeaders(), false)
	})
}

// -- OnResponseHeaders --

func TestOnResponseHeaders_RecordsHistogramAndStatus(t *testing.T) {
	fac := newFactory(t)
	h := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{":method": "GET", ":path": "/"}),
		fake.WithResponseHeaders(map[string]string{":status": "200"}),
	)
	filter := fac.Create(h)
	filter.OnRequestHeaders(h.RequestHeaders(), false)

	// Small sleep to ensure elapsed > 0.
	time.Sleep(2 * time.Millisecond)

	status := filter.OnResponseHeaders(h.ResponseHeaders(), false)
	assert.Equal(t, shared.HeadersStatusContinue, status)

	// Histogram recorded: at least 1 increment (request counter) + 1 histogram.
	var histSeen bool
	for _, ci := range h.CounterIncrements {
		if ci.Hist {
			histSeen = true
			assert.GreaterOrEqual(t, ci.N, uint64(1))
		}
	}
	assert.True(t, histSeen, "expected histogram record")

	// Status metadata set.
	statusBuf, ok := h.GetMetadataString(shared.MetadataSourceTypeDynamic, "luwes", "status")
	require.True(t, ok)
	assert.Equal(t, "200", statusBuf.ToUnsafeString())
}

func TestOnResponseHeaders_ZeroElapsed_ClampedToOne(t *testing.T) {
	// When elapsed rounds to 0 ms, the filter clamps to 1 to avoid skewing
	// percentiles. Use a filter constructed with start already set to now.
	fac := newFactory(t)
	h := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{":method": "GET", ":path": "/"}),
		fake.WithResponseHeaders(map[string]string{":status": "204"}),
	)
	filter := fac.Create(h)
	// OnRequestHeaders sets f.start = time.Now().
	filter.OnRequestHeaders(h.RequestHeaders(), false)
	// Call OnResponseHeaders immediately (no sleep): elapsed likely 0 ms.
	filter.OnResponseHeaders(h.ResponseHeaders(), false)

	var minN uint64 = 9999
	for _, ci := range h.CounterIncrements {
		if ci.Hist && ci.N < minN {
			minN = ci.N
		}
	}
	assert.GreaterOrEqual(t, minN, uint64(1), "histogram value must be >= 1")
}

// -- passive callbacks --

func TestPassiveCallbacks(t *testing.T) {
	fac := newFactory(t)
	h := fake.NewFilterHandle()
	filter := fac.Create(h)

	assert.Equal(t, shared.BodyStatusDefault, filter.OnRequestBody(nil, false))
	assert.Equal(t, shared.TrailersStatusDefault, filter.OnRequestTrailers(nil))
	assert.Equal(t, shared.BodyStatusDefault, filter.OnResponseBody(nil, false))
	assert.Equal(t, shared.TrailersStatusDefault, filter.OnResponseTrailers(nil))
	filter.OnStreamComplete()
	filter.OnDestroy()
}

// -- fakeConfigHandle --

type fakeConfigHandle struct {
	defineCount int
}

func (h *fakeConfigHandle) DefineCounter(name string, tagKeys ...string) (shared.MetricID, shared.MetricsResult) {
	h.defineCount++
	return shared.MetricID(h.defineCount), shared.MetricsSuccess
}
func (h *fakeConfigHandle) DefineHistogram(name string, tagKeys ...string) (shared.MetricID, shared.MetricsResult) {
	h.defineCount++
	return shared.MetricID(h.defineCount), shared.MetricsSuccess
}
func (h *fakeConfigHandle) DefineGauge(name string, tagKeys ...string) (shared.MetricID, shared.MetricsResult) {
	return 0, shared.MetricsSuccess
}
func (h *fakeConfigHandle) Log(_ shared.LogLevel, _ string, _ ...any) {}
func (h *fakeConfigHandle) GetScheduler() shared.Scheduler            { return nil }
func (h *fakeConfigHandle) HttpCallout(_ string, _ [][2]string, _ []byte, _ uint64, _ shared.HttpCalloutCallback) (shared.HttpCalloutInitResult, uint64) {
	return shared.HttpCalloutInitClusterNotFound, 0
}
func (h *fakeConfigHandle) StartHttpStream(_ string, _ [][2]string, _ []byte, _ bool, _ uint64, _ shared.HttpStreamCallback) (shared.HttpCalloutInitResult, uint64) {
	return shared.HttpCalloutInitClusterNotFound, 0
}
func (h *fakeConfigHandle) SendHttpStreamData(_ uint64, _ []byte, _ bool) bool  { return false }
func (h *fakeConfigHandle) SendHttpStreamTrailers(_ uint64, _ [][2]string) bool { return false }
func (h *fakeConfigHandle) ResetHttpStream(_ uint64)                            {}

// -- fakeSpan --

type fakeChildSpan struct {
	finished bool
}

func (s *fakeChildSpan) Finish()                      { s.finished = true }
func (s *fakeChildSpan) SetTag(key, value string)     {}
func (s *fakeChildSpan) SetOperation(name string)     {}
func (s *fakeChildSpan) SetSampled(sampled bool)      {}
func (s *fakeChildSpan) Log(_ string)                 {}
func (s *fakeChildSpan) SetBaggage(key, value string) {}
func (s *fakeChildSpan) GetBaggage(key string) (shared.UnsafeEnvoyBuffer, bool) {
	return shared.UnsafeEnvoyBuffer{}, false
}
func (s *fakeChildSpan) GetSpanID() (shared.UnsafeEnvoyBuffer, bool) {
	return shared.UnsafeEnvoyBuffer{}, false
}
func (s *fakeChildSpan) GetTraceID() (shared.UnsafeEnvoyBuffer, bool) {
	return shared.UnsafeEnvoyBuffer{}, false
}
func (s *fakeChildSpan) SpawnChild(name string) shared.ChildSpan { return nil }

type fakeSpan struct {
	tagSet       bool
	childSpawned bool
	nilChild     bool
	child        *fakeChildSpan
}

func (s *fakeSpan) SetTag(key, value string)   { s.tagSet = true }
func (s *fakeSpan) SetOperation(name string)   {}
func (s *fakeSpan) SetSampled(sampled bool)    {}
func (s *fakeSpan) Log(_ string)               {}
func (s *fakeSpan) SetBaggage(key, val string) {}
func (s *fakeSpan) GetBaggage(key string) (shared.UnsafeEnvoyBuffer, bool) {
	return shared.UnsafeEnvoyBuffer{}, false
}
func (s *fakeSpan) GetSpanID() (shared.UnsafeEnvoyBuffer, bool) {
	return shared.UnsafeEnvoyBuffer{}, false
}
func (s *fakeSpan) GetTraceID() (shared.UnsafeEnvoyBuffer, bool) {
	return shared.UnsafeEnvoyBuffer{}, false
}
func (s *fakeSpan) SpawnChild(name string) shared.ChildSpan {
	s.childSpawned = true
	if s.nilChild {
		return nil
	}
	s.child = &fakeChildSpan{}
	return s.child
}
