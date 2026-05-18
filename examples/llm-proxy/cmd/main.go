// Package main builds the llm-proxy dynamic module.
package main

import (
	sdk "github.com/dio/luwes"
	_ "github.com/dio/luwes/abi_impl"
	llmproxy "github.com/dio/luwes/examples/llm-proxy"
)

func init() {
	sdk.Register("llm-proxy", llmproxy.NewFactory)
	sdk.RegisterHttpFilterConfigFactories(sdk.Factories())
}

func main() {}
