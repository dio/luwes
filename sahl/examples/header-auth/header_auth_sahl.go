// Package headerauthsahl implements the same header-auth filter as
// examples/header-auth, but using the sahl ergonomic layer instead of
// the raw luwes SDK.
//
// Compare the two: the sahl version has no pool, no struct, no
// EmptyHttpFilter embedding. Just a function.
package headerauthsahl

import (
	"net/http"

	"github.com/dio/luwes/sahl"
)

// Handler rejects requests missing x-api-key and injects x-user-id.
// Peek is zero-alloc: the returned string points into Envoy memory,
// valid only for this callback lifetime.
func Handler(w *sahl.Writer, r *sahl.Request) {
	key, ok := r.Header.Peek("x-api-key")
	if !ok || len(key) == 0 {
		w.Send(http.StatusUnauthorized, `{"error":"missing x-api-key"}`)
		return
	}
	w.SetRequestHeader("x-user-id", key)
}
