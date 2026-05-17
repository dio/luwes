package sahl

// Test helpers: exported only for use in sahl_test and example tests.
// Not part of the public API.
//
// Cross-package test helpers (visible from any *_test.go in any package)
// live in sahl/testing.go (NewFilterForTesting, SahlFilterForTesting) and
// sahl/testutil (Filter wrapper). The helpers below are white-box only:
// they access unexported types and are only compiled into the sahl test binary.

import (
	"github.com/dio/luwes/shared"
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

// ParseStatusForTest exposes parseStatus for unit tests.
func ParseStatusForTest(s string) int { return parseStatus(s) }

// FlushForTest applies queued mutations directly (without calling ContinueRequest).
// For use in unit tests that check mutation state without a real Envoy scheduler.
func (w *Writer) FlushForTest() {
	w.flush(false)
}
