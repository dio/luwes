// Binary auth is the Envoy dynamic module entry point for the auth filter.
//
// Build:
//
//	go build -buildmode=c-shared -o auth.so ./cmd
//
// The resulting auth.so is referenced from envoy.yaml.
package main

import (
	sdk "github.com/dio/luwes"
	_ "github.com/dio/luwes/abi_impl"
	_ "github.com/dio/luwes/examples/sahl/auth"

	"github.com/dio/luwes/sahl"
)

func init() {
	sdk.RegisterHttpFilterConfigFactories(sahl.Factories())
}

func main() {}
