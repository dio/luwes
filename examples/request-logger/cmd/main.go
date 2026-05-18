// Package main builds the request-logger dynamic module.
package main

import (
	sdk "github.com/dio/luwes"
	_ "github.com/dio/luwes/abi_impl"
	requestlogger "github.com/dio/luwes/examples/request-logger"
)

func init() {
	sdk.Register("request-logger", requestlogger.NewFactory)
	sdk.RegisterHttpFilterConfigFactories(sdk.Factories())
}

func main() {}
