package main

import (
	_ "github.com/dio/luwes/abi_impl"
	sdk "github.com/dio/luwes"
	headerauth "github.com/dio/luwes/examples/header-auth"
	"github.com/dio/luwes/shared"
)

func init() {
	sdk.RegisterHttpFilterConfigFactories(map[string]shared.HttpFilterConfigFactory{
		"header-auth": &configFactory{},
	})
}

type configFactory struct{}

func (f *configFactory) Create(h shared.HttpFilterConfigHandle, raw []byte) (shared.HttpFilterFactory, error) {
	return headerauth.NewFactory(h, raw)
}

func (f *configFactory) CreatePerRoute(_ []byte) (any, error) { return nil, nil }

func main() {}
