package main

import (
	sdk "github.com/dio/luwes"
	_ "github.com/dio/luwes/abi_impl"
	obs "github.com/dio/luwes/examples/observability"
)

func init() {
	sdk.Register("observability", obs.NewFactory)
	sdk.StartPprof("")
	sdk.RegisterHttpFilterConfigFactories(sdk.Factories())
}

func main() {}
