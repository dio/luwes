package decoder

import "github.com/dio/luwes/sahl"

// DecoderResponseForTest exposes decoderResponse for integration tests.
var DecoderResponseForTest sahl.ResponseHandlerFunc = decoderResponse
