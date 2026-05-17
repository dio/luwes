package sahl

// Test helpers: exported only for use in sahl_test and example tests.
// Not part of the public API.

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

// NewFilterForTest constructs a sahlFilter for use in tests, bypassing the pool.
func NewFilterForTest(name string, handler HandlerFunc, handle shared.HttpFilterHandle) *sahlFilter {
	return &sahlFilter{
		name:    name,
		handler: &filterDef{handler: handler},
		handle:  handle,
	}
}

// NewFilterWithResponseForTest constructs a sahlFilter with a response observer for tests.
func NewFilterWithResponseForTest(name string, handler HandlerFunc, resp ResponseHandlerFunc, handle shared.HttpFilterHandle) *sahlFilter {
	return &sahlFilter{
		name:    name,
		handler: &filterDef{handler: handler, responseFn: resp},
		handle:  handle,
	}
}

// NewBodyAwareFilterForTest constructs a sahlFilter with bodyAware=true for tests.
func NewBodyAwareFilterForTest(name string, handler HandlerFunc, handle shared.HttpFilterHandle) *sahlFilter {
	return &sahlFilter{
		name:    name,
		handler: &filterDef{handler: handler, bodyAware: true},
		handle:  handle,
	}
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
