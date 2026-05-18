package main

import (
	sdk "github.com/dio/luwes"

	"github.com/dio/luwes/sahl"

	headerauth "github.com/dio/luwes/sahl/examples/header-auth"
)

func main() {
	sdk.RegisterRaw("header-auth", sahl.Factory(headerauth.Handler))
}
