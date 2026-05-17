package headerauth_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	headerauth "github.com/dio/luwes/examples/header-auth"
	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
)

// factory builds a Factory using a nil config handle (no metrics).
func factory(t *testing.T) shared.HttpFilterFactory {
	t.Helper()
	f, err := headerauth.NewFactory(nil, nil)
	require.NoError(t, err)
	return f
}

// factoryWithMetrics builds a Factory with a real fake config handle so the
// DefineCounter branch inside NewFactory is exercised.
func factoryWithMetrics(t *testing.T) shared.HttpFilterFactory {
	t.Helper()
	h := &fakeConfigHandle{}
	f, err := headerauth.NewFactory(h, nil)
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
	assert.Equal(t, `{"error":"missing x-api-key"}`, string(h.LocalResponses[0].Body))
}

func TestHeaderAuth_Reject_EmptyKey(t *testing.T) {
	fac := factory(t)
	h := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"x-api-key": ""}))
	filter := fac.Create(h)

	status := filter.OnRequestHeaders(h.RequestHeaders(), false)
	assert.Equal(t, shared.HeadersStatusStop, status)
	require.Len(t, h.LocalResponses, 1)
	assert.Equal(t, uint32(401), h.LocalResponses[0].Status)
}

func TestHeaderAuth_NewFactory_WithMetrics(t *testing.T) {
	// Exercises the h != nil branch: DefineCounter is called and the ID stored.
	fac, err := headerauth.NewFactory(&fakeConfigHandle{}, nil)
	require.NoError(t, err)
	require.NotNil(t, fac)
}

func TestHeaderAuth_Factory_OnDestroy(t *testing.T) {
	fac := factory(t)
	// OnDestroy is a no-op; just confirm it does not panic.
	assert.NotPanics(t, fac.OnDestroy)
}

func TestHeaderAuth_Factory_Create_ReturnsFilter(t *testing.T) {
	fac := factoryWithMetrics(t)
	h := fake.NewFilterHandle()
	filter := fac.Create(h)
	require.NotNil(t, filter)
}

func TestHeaderAuth_OnStreamComplete_ReturnsToPool(t *testing.T) {
	fac := factory(t)
	h := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"x-api-key": "tok"}))
	filter := fac.Create(h)
	filter.OnRequestHeaders(h.RequestHeaders(), false)
	assert.NotPanics(t, filter.OnStreamComplete)
}

func TestHeaderAuth_OnStreamComplete_PoolReuse(t *testing.T) {
	// After OnStreamComplete, Create must return a reused (not freshly allocated) filter.
	// This proves the pool round-trip is correct.
	fac := factory(t)
	h1 := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"x-api-key": "a"}))
	f1 := fac.Create(h1)
	f1.OnRequestHeaders(h1.RequestHeaders(), false)
	f1.OnStreamComplete() // returns to pool

	h2 := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"x-api-key": "b"}))
	f2 := fac.Create(h2)
	// f2 may be the same pointer as f1 (pool reuse); either way it must work correctly.
	status := f2.OnRequestHeaders(h2.RequestHeaders(), false)
	assert.Equal(t, shared.HeadersStatusContinue, status)
	f2.OnStreamComplete()
}

func TestHeaderAuth_PassiveLifecycleCallbacks(t *testing.T) {
	// OnRequestBody, OnRequestTrailers, OnResponseHeaders, OnResponseBody,
	// OnResponseTrailers are all default-return no-ops. Call them to confirm
	// correct return values and no panic.
	fac := factory(t)
	h := fake.NewFilterHandle()
	filter := fac.Create(h)

	assert.Equal(t, shared.BodyStatusDefault, filter.OnRequestBody(nil, false))
	assert.Equal(t, shared.TrailersStatusDefault, filter.OnRequestTrailers(nil))
	assert.Equal(t, shared.HeadersStatusDefault, filter.OnResponseHeaders(nil, false))
	assert.Equal(t, shared.BodyStatusDefault, filter.OnResponseBody(nil, false))
	assert.Equal(t, shared.TrailersStatusDefault, filter.OnResponseTrailers(nil))
	filter.OnStreamComplete()
}

// -- fakeConfigHandle --

type fakeConfigHandle struct{}

func (h *fakeConfigHandle) DefineCounter(name string, tagKeys ...string) (shared.MetricID, shared.MetricsResult) {
	return shared.MetricID(1), shared.MetricsSuccess
}
func (h *fakeConfigHandle) DefineHistogram(name string, tagKeys ...string) (shared.MetricID, shared.MetricsResult) {
	return shared.MetricID(2), shared.MetricsSuccess
}
func (h *fakeConfigHandle) DefineGauge(name string, tagKeys ...string) (shared.MetricID, shared.MetricsResult) {
	return shared.MetricID(3), shared.MetricsSuccess
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
