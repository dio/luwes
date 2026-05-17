package ssetap

import "github.com/dio/luwes/sahl"

// TapResponseForTest exposes tapResponse for integration tests.
// Allows tests to drive the full response observer via sahl/testutil.NewFilterWithResponse
// without a real Envoy process.
var TapResponseForTest sahl.ResponseHandlerFunc = tapResponse
