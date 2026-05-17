package main

import (
	sdk "github.com/dio/luwes"

	"github.com/dio/luwes/sahl"

	headerauthsahl "github.com/dio/luwes/examples/header-auth-sahl"
)

func main() {
	sdk.RegisterRaw("header-auth-sahl", sahl.Factory(headerauthsahl.Handler))
}
