package spa_test

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"testing"

	spa "github.com/dio/luwes/examples/sahl/spa"
	"github.com/dio/luwes/sahl"
	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildFactory creates a sahl filter factory for the given handler.
func buildFactory(t *testing.T, name string) shared.HttpFilterFactory {
	t.Helper()
	factories := sahl.Factories()
	def, ok := factories[name]
	require.True(t, ok, "filter %q not registered", name)
	factory, err := def.Create(&fakeConfigHandle{}, nil)
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

// runFilter runs a filter through OnRequestHeaders and returns the result.
func runFilter(t *testing.T, name, path string) *fake.FakeFilterHandle {
	t.Helper()
	factory := buildFactory(t, name)
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{
		":method": "GET",
		":path":   path,
	}))
	f := factory.Create(fh)
	f.OnRequestHeaders(fh.RequestHeaders(), false)
	f.OnStreamComplete()
	f.OnDestroy()
	return fh
}

// assetPath returns the first embedded asset path matching the given extension.
// Vite fingerprints filenames (e.g. index-4zgxRtfG.css), so tests must
// discover the actual filename at runtime.
func assetPath(t *testing.T, ext string) string {
	t.Helper()
	var found string
	fs.WalkDir(spa.UIFS, "ui/dist/assets", func(path string, d fs.DirEntry, err error) error { //nolint:errcheck
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(path, ext) && found == "" {
			found = strings.TrimPrefix(path, "ui/dist")
		}
		return nil
	})
	require.NotEmpty(t, found, "no %s asset found in ui/dist/assets", ext)
	return found
}

// -- spa filter tests --

func TestSPA_Root_ServesIndexHTML(t *testing.T) {
	fh := runFilter(t, "spa", "/")
	require.Len(t, fh.LocalResponses, 1)
	lr := fh.LocalResponses[0]
	assert.Equal(t, uint32(http.StatusOK), lr.Status)
	assert.Contains(t, string(lr.Body), "<html", "root should serve HTML")
	assert.Contains(t, localResponseHeader(lr, "content-type"), "text/html")
	assert.Equal(t, "no-cache", localResponseHeader(lr, "cache-control"))
}

func TestSPA_UnknownPath_FallsBackToIndexHTML(t *testing.T) {
	for _, path := range []string{"/about", "/dashboard", "/some/deep/path"} {
		fh := runFilter(t, "spa", path)
		require.Len(t, fh.LocalResponses, 1, "path=%s", path)
		assert.Equal(t, uint32(http.StatusOK), fh.LocalResponses[0].Status, "path=%s", path)
		assert.Contains(t, string(fh.LocalResponses[0].Body), "<html", "path=%s", path)
	}
}

func TestSPA_JSAsset_ServedWithImmutableCache(t *testing.T) {
	jsPath := assetPath(t, ".js")
	fh := runFilter(t, "spa", jsPath)
	require.Len(t, fh.LocalResponses, 1)
	lr := fh.LocalResponses[0]
	assert.Equal(t, uint32(http.StatusOK), lr.Status)
	assert.Contains(t, localResponseHeader(lr, "cache-control"), "immutable")
	assert.NotEmpty(t, lr.Body)
}

func TestSPA_CSSAsset_CorrectContentType(t *testing.T) {
	cssPath := assetPath(t, ".css")
	fh := runFilter(t, "spa", cssPath)
	require.Len(t, fh.LocalResponses, 1)
	assert.Contains(t, localResponseHeader(fh.LocalResponses[0], "content-type"), "text/css")
}

func TestSPA_Favicon_Served(t *testing.T) {
	fh := runFilter(t, "spa", "/favicon.svg")
	require.Len(t, fh.LocalResponses, 1)
	assert.Equal(t, uint32(http.StatusOK), fh.LocalResponses[0].Status)
}

func TestSPA_QueryString_Stripped(t *testing.T) {
	fh := runFilter(t, "spa", "/?foo=bar")
	require.Len(t, fh.LocalResponses, 1)
	assert.Contains(t, string(fh.LocalResponses[0].Body), "<html")
}

// -- api-backend filter tests --

func TestAPI_Hello(t *testing.T) {
	fh := runFilter(t, "api-backend", "/api/hello")
	require.Len(t, fh.LocalResponses, 1)
	lr := fh.LocalResponses[0]
	assert.Equal(t, uint32(http.StatusOK), lr.Status)
	assert.Contains(t, localResponseHeader(lr, "content-type"), "application/json")
	var body map[string]any
	require.NoError(t, json.Unmarshal(lr.Body, &body))
	assert.Equal(t, "hello from inside the .so", body["message"])
}

func TestAPI_Time(t *testing.T) {
	fh := runFilter(t, "api-backend", "/api/time")
	require.Len(t, fh.LocalResponses, 1)
	var body map[string]any
	require.NoError(t, json.Unmarshal(fh.LocalResponses[0].Body, &body))
	assert.NotEmpty(t, body["time"])
}

func TestAPI_Unknown_Returns404(t *testing.T) {
	fh := runFilter(t, "api-backend", "/api/unknown-endpoint")
	require.Len(t, fh.LocalResponses, 1)
	assert.Equal(t, uint32(http.StatusNotFound), fh.LocalResponses[0].Status)
}

func TestAPI_NonAPIPath_PassThrough(t *testing.T) {
	// Non-/api/ path: api-backend must not respond (pass through to spa filter).
	fh := runFilter(t, "api-backend", "/about")
	assert.Empty(t, fh.LocalResponses, "/about should not be handled by api-backend")
}

func TestAPI_HelloWithQueryString(t *testing.T) {
	fh := runFilter(t, "api-backend", "/api/hello?name=world")
	require.Len(t, fh.LocalResponses, 1)
	assert.Equal(t, uint32(http.StatusOK), fh.LocalResponses[0].Status)
}

// localResponseHeader returns the value of a response header from a LocalResponse.
func localResponseHeader(lr fake.LocalResponse, key string) string {
	for _, h := range lr.Headers {
		if strings.EqualFold(h[0], key) {
			return h[1]
		}
	}
	return ""
}
