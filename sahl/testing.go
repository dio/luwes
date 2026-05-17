package sahl

// This file exports constructors intended for use in tests only.
// Do not import sahl from production code and call these.

import "github.com/dio/luwes/shared"

// SahlFilterForTesting is the concrete filter type returned by NewFilterForTesting.
// It implements shared.HttpFilter. Exported so sahl/testutil can wrap it without
// repeating the method set.
type SahlFilterForTesting = sahlFilter

// NewFilterForTesting constructs a filter instance for unit and integration tests.
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
