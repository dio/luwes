package main

import (
	sdk "github.com/dio/luwes"
	_ "github.com/dio/luwes/abi_impl"
	hello "github.com/dio/luwes/examples/hello"
)

func init() {
	sdk.Register("hello", hello.NewFactory)
	sdk.RegisterHttpFilterConfigFactories(sdk.Factories())
}

func main() {}
