// Package main builds the header-auth dynamic module.
//
// All wiring is two lines: Register the filter, start pprof.
// The filter package's NewFactory does the rest.
package main

import (
	sdk "github.com/dio/luwes"
	_   "github.com/dio/luwes/abi_impl"
	headerauth "github.com/dio/luwes/examples/header-auth"
)

func init() {
	sdk.Register("header-auth", headerauth.NewFactory)
	sdk.StartPprof("")
	sdk.RegisterHttpFilterConfigFactories(sdk.Factories())
}

func main() {}
