// Package main builds the request-logger dynamic module.
package main

import (
	sdk "github.com/dio/luwes"
	_ "github.com/dio/luwes/abi_impl"
	requestlogger "github.com/dio/luwes/examples/request-logger"
	"github.com/dio/luwes/shared"
)

func init() {
	// sharedFactory is created once per config load and shared between the HTTP
	// filter and the access logger so they can exchange records via pendingRecords.
	var sharedFactory *requestlogger.Factory

	sdk.Register("request-logger", func(h shared.HttpFilterConfigHandle, raw []byte) (shared.HttpFilterFactory, error) {
		f, err := requestlogger.NewFactory(h, raw)
		if err != nil {
			return nil, err
		}
		sharedFactory = f.(*requestlogger.Factory)
		return sharedFactory, nil
	})

	sdk.RegisterAccessLogger("request-logger", func(h shared.AccessLoggerConfigHandle, raw []byte) (shared.AccessLoggerFactory, error) {
		// Access logger config is loaded on the main thread, same as the HTTP
		// filter config. sharedFactory is populated first (Envoy loads HTTP filter
		// config before access logger config). Guard against the unlikely inverse.
		if sharedFactory == nil {
			sharedFactory = &requestlogger.Factory{}
		}
		return requestlogger.NewAccessLoggerFactory(sharedFactory)(h, raw)
	})

	sdk.RegisterHttpFilterConfigFactories(sdk.Factories())
	sdk.RegisterAccessLoggerConfigFactories(sdk.AccessLoggerFactories())
}

func main() {}
