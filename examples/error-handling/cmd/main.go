package main

import (
	sdk "github.com/dio/luwes"
	_ "github.com/dio/luwes/abi_impl"
	errorhandling "github.com/dio/luwes/examples/error-handling"
)

func init() {
	sdk.Register("error-handling", errorhandling.NewFactory)
	sdk.RegisterHttpFilterConfigFactories(sdk.Factories())
}

func main() {}
