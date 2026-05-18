package calloutfilters

// mutableBodyHandler is a RegisterWithMutableResponse filter.
//
// On EndStream it replaces the entire response body with a JSON envelope that
// includes the observed StatusCode and ResponseFlags. This lets e2e tests verify
// both body mutation and response-flag classification in one round-trip.
//
// Routes (controlled by the Envoy route table):
//   GET /ok         -> callout_upstream (200, no flags)
//   GET /infra-fail -> dead_upstream   (503, UF flag from Envoy)

import (
	"fmt"

	"github.com/dio/luwes/sahl"
)

func mutableBodyResponseHandler(w *sahl.Writer, chunk *sahl.ResponseChunk) {
	if !chunk.EndStream {
		return
	}
	body := fmt.Sprintf(`{"status":%d,"flags":%q}`, chunk.StatusCode, chunk.ResponseFlags)
	w.SetResponseBody([]byte(body))
}

func mutableBodyRequestHandler(w *sahl.Writer, r *sahl.Request) {}

func init() {
	sahl.RegisterWithMutableResponse("mutable-body-sahl", mutableBodyRequestHandler, mutableBodyResponseHandler)
}
