package sahl

// Test helpers: exported only for use in sahl_test and example tests.
// Not part of the public API.
//
// Cross-package test helpers (visible from any *_test.go in any package)
// live in sahl/testing.go (NewFilterForTesting, SahlFilterForTesting) and
// sahl/testutil (Filter wrapper). The helpers below are white-box only:
// they access unexported types and are only compiled into the sahl test binary.

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
)

// NewRequestForTest constructs a Request for use in tests, bypassing the pool.
func NewRequestForTest(hm shared.HeaderMap, handle shared.HttpFilterHandle, name string) *Request {
	r := &Request{}
	r.reset(hm, handle, name)
	return r
}

// NewWriterForTest constructs a Writer for use in tests, bypassing the pool.
func NewWriterForTest(handle shared.HttpFilterHandle, scheduler shared.Scheduler) *Writer {
	w := &Writer{}
	w.reset(handle, scheduler)
	return w
}

// NewFilterForTest constructs a sahlFilter for tests, bypassing the pool.
// For cross-package use, prefer sahl/testutil.NewFilter instead.
func NewFilterForTest(name string, handler HandlerFunc, handle shared.HttpFilterHandle) *sahlFilter {
	return NewFilterForTesting(name, handler, nil, false, handle)
}

// NewFilterWithResponseForTest constructs a sahlFilter with a response observer.
// For cross-package use, prefer sahl/testutil.NewFilterWithResponse instead.
func NewFilterWithResponseForTest(name string, handler HandlerFunc, resp ResponseHandlerFunc, handle shared.HttpFilterHandle) *sahlFilter {
	return NewFilterForTesting(name, handler, resp, false, handle)
}

// NewBodyAwareFilterForTest constructs a sahlFilter with bodyAware=true.
// For cross-package use, prefer sahl/testutil.NewBodyAwareFilter instead.
func NewBodyAwareFilterForTest(name string, handler HandlerFunc, handle shared.HttpFilterHandle) *sahlFilter {
	return NewFilterForTesting(name, handler, nil, true, handle)
}

// Responded reports whether Send or SendBytes was called.
func (w *Writer) Responded() bool { return w.responded }

// TestNewWriterForTesting_Functional verifies NewWriterForTesting from sahl/testing.go
// returns a Writer that can queue and flush mutations. Lives here (package sahl) so
// the function is counted in the sahl coverage run; its external consumer is
// sahl/examples/decoder which exercises it from a different package.
func TestNewWriterForTesting_Functional(t *testing.T) {
	t.Helper() // suppress output on success
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"x-test": ""}))
	w := NewWriterForTesting(fh)
	if w == nil {
		t.Fatal("NewWriterForTesting returned nil")
	}
	w.SetRequestHeader("x-test", "from-writer")
	w.flush(false)
	if got := fh.RequestHeaders().(*fake.FakeHeaderMap).GetString("x-test"); got != "from-writer" {
		t.Fatalf("want x-test=from-writer, got %q", got)
	}
}

// TestConfigFactory_NilHandler_Panics covers the def.handler == nil guard in
// configFactory.Create. A nil handler is a programmer error (nil passed to
// Factory or Register*) caught at config-load time as a BUG panic rather than
// a recoverable error, so it never silently reaches request handling.
func TestConfigFactory_NilHandler_Panics(t *testing.T) {
	cf := newConfigFactory("bad-filter", &filterDef{handler: nil})
	assert.Panics(t, func() {
		_, _ = cf.Create(&fakeConfigHandleForExport{}, nil)
	})
}

type fakeConfigHandleForExport struct{}

func (h *fakeConfigHandleForExport) DefineCounter(_ string, _ ...string) (shared.MetricID, shared.MetricsResult) {
	return 0, shared.MetricsSuccess
}
func (h *fakeConfigHandleForExport) DefineHistogram(_ string, _ ...string) (shared.MetricID, shared.MetricsResult) {
	return 0, shared.MetricsSuccess
}
func (h *fakeConfigHandleForExport) DefineGauge(_ string, _ ...string) (shared.MetricID, shared.MetricsResult) {
	return 0, shared.MetricsSuccess
}
func (h *fakeConfigHandleForExport) Log(_ shared.LogLevel, _ string, _ ...any) {}
func (h *fakeConfigHandleForExport) GetScheduler() shared.Scheduler            { return nil }
func (h *fakeConfigHandleForExport) HttpCallout(_ string, _ [][2]string, _ []byte, _ uint64, _ shared.HttpCalloutCallback) (shared.HttpCalloutInitResult, uint64) {
	return shared.HttpCalloutInitClusterNotFound, 0
}
func (h *fakeConfigHandleForExport) StartHttpStream(_ string, _ [][2]string, _ []byte, _ bool, _ uint64, _ shared.HttpStreamCallback) (shared.HttpCalloutInitResult, uint64) {
	return shared.HttpCalloutInitClusterNotFound, 0
}
func (h *fakeConfigHandleForExport) SendHttpStreamData(_ uint64, _ []byte, _ bool) bool { return false }
func (h *fakeConfigHandleForExport) SendHttpStreamTrailers(_ uint64, _ [][2]string) bool {
	return false
}
func (h *fakeConfigHandleForExport) ResetHttpStream(_ uint64) {}
func ParseStatusForTest(s string) int                         { return parseStatus(s) }

// FlushForTest applies queued mutations directly (without calling ContinueRequest).
// For use in unit tests that check mutation state without a real Envoy scheduler.
func (w *Writer) FlushForTest() {
	w.flush(false)
}
