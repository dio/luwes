// Package main builds the combined e2e dynamic module.
// Registers all filters exercised by the e2e test suite.
package main

import (
	sdk "github.com/dio/luwes"
	_ "github.com/dio/luwes/abi_impl"
	"github.com/dio/luwes/sahl"

	_ "github.com/dio/luwes/e2e/calloutfilters" // registers callout-sahl, stream-sahl, do-sahl
	headerauth "github.com/dio/luwes/examples/header-auth"
	_ "github.com/dio/luwes/sahl/examples/auth" // registers via init()
	headerauthsahl "github.com/dio/luwes/sahl/examples/header-auth"
	_ "github.com/dio/luwes/sahl/examples/sse-tap" // registers via init()
)

func init() {
	// Raw luwes SDK: header-auth filter (port 10000 in e2e).
	sdk.Register("header-auth", headerauth.NewFactory)

	// sahl ergonomic layer: same contract, functional handler (port 10001 in e2e).
	sdk.RegisterRaw("header-auth-sahl", sahl.Factory(headerauthsahl.Handler))

	// sahl sse-tap: response observer, SSE token extraction (port 10002 in e2e).
	// Registered via init() in sahl/examples/sse-tap.
	sdk.RegisterHttpFilterConfigFactories(sahl.Factories())

	sdk.RegisterHttpFilterConfigFactories(sdk.Factories())
}

func main() {}
