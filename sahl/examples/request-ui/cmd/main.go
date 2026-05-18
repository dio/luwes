// Package main builds the request-ui dynamic module.
//
// Starts the Postgres-backed sink (HTTP server + SSE broadcaster + writer goroutine)
// then registers the sahl filter and access logger that feed records into it.
package main

import (
	sdk "github.com/dio/luwes"
	_ "github.com/dio/luwes/abi_impl"
	requestui "github.com/dio/luwes/sahl/examples/request-ui"
	"github.com/dio/luwes/sahl/examples/request-ui/sink"
	"github.com/dio/luwes/shared"

	"github.com/dio/luwes/sahl"
)

var (
	globalSink    = sink.New()
	globalPending = &requestui.PendingRecords{}
)

func init() {
	// Start the sink: connects to Postgres, starts writer goroutine + HTTP server.
	// Reads REQUI_DSN and REQUI_ADDR from the environment.
	globalSink.Start()

	// Register the HTTP filter. It deposits partial records into globalPending;
	// the access logger below pops, enriches, and sends them to globalSink.
	requestui.Register("request-ui", globalSink, globalPending)

	// Register the access logger. Fires after stream finalization and fills in
	// duration, byte counts, response flags, and code_details.
	sdk.RegisterAccessLogger("request-ui", func(_ shared.AccessLoggerConfigHandle, raw []byte) (shared.AccessLoggerFactory, error) {
		return requestui.NewAccessLoggerFactory(globalPending, globalSink)(nil, raw)
	})

	sdk.RegisterHttpFilterConfigFactories(sahl.Factories())
	sdk.RegisterAccessLoggerConfigFactories(sdk.AccessLoggerFactories())
}

func main() {}
