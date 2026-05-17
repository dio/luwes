// Package main builds the combined e2e dynamic module.
// Registers all filters exercised by the e2e test suite.
package main

import (
	sdk "github.com/dio/luwes"
	_ "github.com/dio/luwes/abi_impl"
	"github.com/dio/luwes/sahl"

	headerauth "github.com/dio/luwes/examples/header-auth"
	headerauthsahl "github.com/dio/luwes/examples/sahl/header-auth"
	_ "github.com/dio/luwes/examples/sahl/auth"    // registers via init()
	_ "github.com/dio/luwes/examples/sahl/sse-tap" // registers via init()
)

func init() {
	// Raw luwes SDK: header-auth filter (port 10000 in e2e).
	sdk.Register("header-auth", headerauth.NewFactory)

	// sahl ergonomic layer: same contract, functional handler (port 10001 in e2e).
	sdk.RegisterRaw("header-auth-sahl", sahl.Factory(headerauthsahl.Handler))

	// sahl sse-tap: response observer, SSE token extraction (port 10002 in e2e).
	// Registered via init() in examples/sahl/sse-tap.
	sdk.RegisterHttpFilterConfigFactories(sahl.Factories())

	sdk.RegisterHttpFilterConfigFactories(sdk.Factories())
}

func main() {}
