package main

import (
	sdk "github.com/dio/luwes"
	_ "github.com/dio/luwes/abi_impl"
	_ "github.com/dio/luwes/sahl/examples/sse-tap"

	"github.com/dio/luwes/sahl"
)

func init() {
	sdk.RegisterHttpFilterConfigFactories(sahl.Factories())
}

func main() {}
