package headerauthsahl_test

import (
	"net/http"
	"testing"

	"github.com/dio/luwes/sahl"
	headerauthsahl "github.com/dio/luwes/sahl/examples/header-auth"
	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildFactory constructs a sahl filter factory from the Handler function.
func buildFactory(t *testing.T) shared.HttpFilterFactory {
	t.Helper()
	cfgFactory := sahl.Factory(headerauthsahl.Handler)
	factory, err := cfgFactory.Create(&fakeConfigHandle{}, nil)
	require.NoError(t, err)
	return factory
}

type fakeConfigHandle struct{}

func (f *fakeConfigHandle) Log(_ shared.LogLevel, _ string, _ ...any) {}
func (f *fakeConfigHandle) DefineCounter(_ string, _ ...string) (shared.MetricID, shared.MetricsResult) {
	return 0, shared.MetricsSuccess
}
func (f *fakeConfigHandle) DefineGauge(_ string, _ ...string) (shared.MetricID, shared.MetricsResult) {
	return 0, shared.MetricsSuccess
}
func (f *fakeConfigHandle) DefineHistogram(_ string, _ ...string) (shared.MetricID, shared.MetricsResult) {
	return 0, shared.MetricsSuccess
}
func (f *fakeConfigHandle) HttpCallout(_ string, _ [][2]string, _ []byte, _ uint64, _ shared.HttpCalloutCallback) (shared.HttpCalloutInitResult, uint64) {
	return shared.HttpCalloutInitClusterNotFound, 0
}
func (f *fakeConfigHandle) StartHttpStream(_ string, _ [][2]string, _ []byte, _ bool, _ uint64, _ shared.HttpStreamCallback) (shared.HttpCalloutInitResult, uint64) {
	return shared.HttpCalloutInitClusterNotFound, 0
}
func (f *fakeConfigHandle) SendHttpStreamData(_ uint64, _ []byte, _ bool) bool  { return false }
func (f *fakeConfigHandle) SendHttpStreamTrailers(_ uint64, _ [][2]string) bool { return false }
func (f *fakeConfigHandle) ResetHttpStream(_ uint64)                            {}
func (f *fakeConfigHandle) GetScheduler() shared.Scheduler                      { return nil }

func TestHandler_Accept(t *testing.T) {
	factory := buildFactory(t)
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{
		"x-api-key":  "my-token",
		":method":    "GET",
		":path":      "/",
		":authority": "localhost",
	}))

	f := factory.Create(fh)
	status := f.OnRequestHeaders(fh.RequestHeaders(), false)
	f.OnStreamComplete()
	f.OnDestroy()

	assert.Equal(t, shared.HeadersStatusContinue, status)
	assert.Empty(t, fh.LocalResponses, "no local response on accept")
	sets := fh.RequestHeaders().(*fake.FakeHeaderMap).Sets
	require.Len(t, sets, 1)
	assert.Equal(t, "x-user-id", sets[0].Key)
	assert.Equal(t, "my-token", sets[0].Value)
}

func TestHandler_Reject_MissingKey(t *testing.T) {
	factory := buildFactory(t)
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{
		":method": "GET",
		":path":   "/",
	}))

	f := factory.Create(fh)
	status := f.OnRequestHeaders(fh.RequestHeaders(), false)
	f.OnStreamComplete()
	f.OnDestroy()

	assert.Equal(t, shared.HeadersStatusStop, status)
	require.Len(t, fh.LocalResponses, 1)
	assert.Equal(t, uint32(http.StatusUnauthorized), fh.LocalResponses[0].Status)
	assert.Equal(t, `{"error":"missing x-api-key"}`, string(fh.LocalResponses[0].Body))
}

func TestHandler_Reject_EmptyKey(t *testing.T) {
	factory := buildFactory(t)
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{
		"x-api-key": "",
		":method":   "GET",
		":path":     "/",
	}))

	f := factory.Create(fh)
	status := f.OnRequestHeaders(fh.RequestHeaders(), false)
	f.OnStreamComplete()
	f.OnDestroy()

	assert.Equal(t, shared.HeadersStatusStop, status)
	require.Len(t, fh.LocalResponses, 1)
	assert.Equal(t, uint32(http.StatusUnauthorized), fh.LocalResponses[0].Status)
}

func TestHandler_PoolReuse(t *testing.T) {
	factory := buildFactory(t)
	for i := range 10 {
		key := "token-" + string(rune('A'+i))
		fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{
			"x-api-key":  key,
			":method":    "GET",
			":path":      "/",
			":authority": "localhost",
		}))
		f := factory.Create(fh)
		status := f.OnRequestHeaders(fh.RequestHeaders(), false)
		f.OnStreamComplete()
		f.OnDestroy()

		assert.Equal(t, shared.HeadersStatusContinue, status)
		sets := fh.RequestHeaders().(*fake.FakeHeaderMap).Sets
		require.Len(t, sets, 1)
		assert.Equal(t, key, sets[0].Value)
	}
}
