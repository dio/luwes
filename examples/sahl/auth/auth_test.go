package auth_test

import (
	"net/http"
	"testing"

	"github.com/dio/luwes/examples/sahl/auth"
	"github.com/dio/luwes/sahl"
	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAuthConfigHandle satisfies sahl.ConfigHandle for tests.
type fakeAuthConfigHandle struct {
	raw []byte
}

func (f *fakeAuthConfigHandle) Log(_ shared.LogLevel, _ string, _ ...any) {}
func (f *fakeAuthConfigHandle) DefineCounter(_ string, _ ...string) (shared.MetricID, shared.MetricsResult) {
	return 0, shared.MetricsSuccess
}
func (f *fakeAuthConfigHandle) DefineGauge(_ string, _ ...string) (shared.MetricID, shared.MetricsResult) {
	return 0, shared.MetricsSuccess
}
func (f *fakeAuthConfigHandle) DefineHistogram(_ string, _ ...string) (shared.MetricID, shared.MetricsResult) {
	return 0, shared.MetricsSuccess
}
func (f *fakeAuthConfigHandle) HttpCallout(_ string, _ [][2]string, _ []byte, _ uint64, _ shared.HttpCalloutCallback) (shared.HttpCalloutInitResult, uint64) {
	return shared.HttpCalloutInitClusterNotFound, 0
}
func (f *fakeAuthConfigHandle) StartHttpStream(_ string, _ [][2]string, _ []byte, _ bool, _ uint64, _ shared.HttpStreamCallback) (shared.HttpCalloutInitResult, uint64) {
	return shared.HttpCalloutInitClusterNotFound, 0
}
func (f *fakeAuthConfigHandle) SendHttpStreamData(_ uint64, _ []byte, _ bool) bool  { return false }
func (f *fakeAuthConfigHandle) SendHttpStreamTrailers(_ uint64, _ [][2]string) bool { return false }
func (f *fakeAuthConfigHandle) ResetHttpStream(_ uint64)                            {}
func (f *fakeAuthConfigHandle) GetScheduler() shared.Scheduler                      { return nil }
func (f *fakeAuthConfigHandle) RawConfig() []byte                                   { return f.raw }

// buildFactory creates the auth filter factory from a JSON config.
func buildFactory(t *testing.T, configJSON string) shared.HttpFilterFactory {
	t.Helper()
	factories := sahl.Factories()
	def, ok := factories[auth.ExtensionName]
	require.True(t, ok, "auth filter not registered")
	factory, err := def.Create(&fakeAuthConfigHandle{raw: []byte(configJSON)}, []byte(configJSON))
	require.NoError(t, err)
	return factory
}

// runAuth runs the auth filter through a single request lifecycle.
func runAuth(t *testing.T, factory shared.HttpFilterFactory, headers map[string]string) *fake.FakeFilterHandle {
	t.Helper()
	fh := fake.NewFilterHandle(fake.WithHeaders(headers))
	f := factory.Create(fh)
	f.OnRequestHeaders(fh.RequestHeaders(), false)
	f.OnStreamComplete()
	f.OnDestroy()
	return fh
}

// -- Tests --

func TestAuth_Allowed(t *testing.T) {
	factory := buildFactory(t, `{"allowed_keys":["key-admin","key-readonly"]}`)
	fh := runAuth(t, factory, map[string]string{
		"x-api-key": "key-admin",
		":method":   "GET", ":path": "/api/data",
	})
	assert.Empty(t, fh.LocalResponses, "valid key must not produce a local response")
	// x-user-id must be injected.
	sets := fh.RequestHeaders().(*fake.FakeHeaderMap).Sets
	found := ""
	for _, s := range sets {
		if s.Key == "x-user-id" {
			found = s.Value
		}
	}
	assert.Equal(t, "key-admin", found)
}

func TestAuth_Rejected_UnknownKey(t *testing.T) {
	factory := buildFactory(t, `{"allowed_keys":["key-admin"]}`)
	fh := runAuth(t, factory, map[string]string{
		"x-api-key": "bad-key",
		":method":   "GET", ":path": "/",
	})
	require.Len(t, fh.LocalResponses, 1)
	assert.Equal(t, uint32(http.StatusUnauthorized), fh.LocalResponses[0].Status)
	assert.Contains(t, string(fh.LocalResponses[0].Body), "invalid")
}

func TestAuth_Rejected_MissingKey(t *testing.T) {
	factory := buildFactory(t, `{"allowed_keys":["key-admin"]}`)
	fh := runAuth(t, factory, map[string]string{
		":method": "GET", ":path": "/",
	})
	require.Len(t, fh.LocalResponses, 1)
	assert.Equal(t, uint32(http.StatusUnauthorized), fh.LocalResponses[0].Status)
	assert.Contains(t, string(fh.LocalResponses[0].Body), "missing")
}

func TestAuth_EmptyConfig_RejectsAll(t *testing.T) {
	// No allowed_keys: every key is rejected.
	factory := buildFactory(t, `{}`)
	fh := runAuth(t, factory, map[string]string{
		"x-api-key": "any-key",
		":method":   "GET", ":path": "/",
	})
	require.Len(t, fh.LocalResponses, 1)
	assert.Equal(t, uint32(http.StatusUnauthorized), fh.LocalResponses[0].Status)
}

// TestAuth_PerListenerIsolation is the core RegisterFactory test.
// Two factory instances created with different configs must be independent:
// key-admin is accepted on instance A but rejected on instance B,
// and key-public is accepted on B but rejected on A.
func TestAuth_PerListenerIsolation(t *testing.T) {
	factoryA := buildFactory(t, `{"allowed_keys":["key-admin"]}`)
	factoryB := buildFactory(t, `{"allowed_keys":["key-public","key-guest"]}`)

	// key-admin: allowed on A, rejected on B.
	fhA := runAuth(t, factoryA, map[string]string{"x-api-key": "key-admin", ":method": "GET"})
	assert.Empty(t, fhA.LocalResponses, "key-admin must pass on factory A")

	fhB := runAuth(t, factoryB, map[string]string{"x-api-key": "key-admin", ":method": "GET"})
	require.Len(t, fhB.LocalResponses, 1, "key-admin must be rejected on factory B")
	assert.Equal(t, uint32(http.StatusUnauthorized), fhB.LocalResponses[0].Status)

	// key-public: rejected on A, allowed on B.
	fhA2 := runAuth(t, factoryA, map[string]string{"x-api-key": "key-public", ":method": "GET"})
	require.Len(t, fhA2.LocalResponses, 1, "key-public must be rejected on factory A")

	fhB2 := runAuth(t, factoryB, map[string]string{"x-api-key": "key-public", ":method": "GET"})
	assert.Empty(t, fhB2.LocalResponses, "key-public must pass on factory B")
}

func TestAuth_PoolReuse_NoStateLeak(t *testing.T) {
	factory := buildFactory(t, `{"allowed_keys":["key-admin"]}`)
	for i := range 10 {
		key := "key-admin"
		if i%2 == 0 {
			key = "bad-key"
		}
		fh := runAuth(t, factory, map[string]string{"x-api-key": key, ":method": "GET"})
		if i%2 == 0 {
			assert.NotEmpty(t, fh.LocalResponses, "iteration %d: bad key must be rejected", i)
		} else {
			assert.Empty(t, fh.LocalResponses, "iteration %d: good key must pass", i)
		}
	}
}
