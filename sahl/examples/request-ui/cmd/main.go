// Package main builds the request-ui dynamic module.
//
// Starts the Postgres-backed sink (HTTP server + SSE broadcaster + writer goroutine)
// then registers the sahl filter that feeds records into it.
package main

import (
	sdk "github.com/dio/luwes"
	_ "github.com/dio/luwes/abi_impl"
	requestui "github.com/dio/luwes/sahl/examples/request-ui"
	"github.com/dio/luwes/sahl/examples/request-ui/sink"

	"github.com/dio/luwes/sahl"
)

var globalSink = sink.New()

func init() {
	// Start the sink: connects to Postgres, starts writer goroutine + HTTP server.
	// Reads REQUI_DSN and REQUI_ADDR from the environment.
	globalSink.Start()

	// Register the sahl filter under the name "request-ui".
	requestui.Register("request-ui", globalSink)

	sdk.RegisterHttpFilterConfigFactories(sahl.Factories())
}

func main() {}
