package sahl

// This file exports constructors intended for use in tests only.
// Do not import sahl from production code and call these.

import "github.com/dio/luwes/shared"

// SahlFilterForTesting is the concrete filter type returned by NewFilterForTesting.
// It implements shared.HttpFilter. Exported so sahl/testutil can wrap it without
// repeating the method set.
type SahlFilterForTesting = sahlFilter

// NewWriterForTesting constructs a Writer for use in tests outside the sahl package.
// The writer is not pooled; the caller owns it.
func NewWriterForTesting(handle shared.HttpFilterHandle) *Writer {
	w := &Writer{}
	w.reset(handle, nil)
	return w
}

// It bypasses the sync.Pool so each call returns a fresh, unconditionally-owned
// instance. Callers are responsible for calling OnDestroy when done.
//
// resp may be nil (no response observer). bodyAware=true makes OnRequestHeaders
// return HeadersStatusStopAllAndBuffer and defers handler execution to OnRequestBody.
func NewFilterForTesting(
	name string,
	h HandlerFunc,
	resp ResponseHandlerFunc,
	bodyAware bool,
	handle shared.HttpFilterHandle,
) *SahlFilterForTesting {
	return &sahlFilter{
		name:    name,
		handler: &filterDef{handler: h, responseFn: resp, bodyAware: bodyAware},
		handle:  handle,
	}
}

// SetMutableResponse switches the filter into mutable-response mode.
// OnResponseBody returns BodyStatusStopAndBuffer on non-final chunks,
// buffering the full response before the handler can mutate it.
// Only valid on filters created via NewFilterForTesting.
func (f *SahlFilterForTesting) SetMutableResponse(v bool) {
	f.handler.mutableResponse = v
}
