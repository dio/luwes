package main

import (
	sdk "github.com/dio/luwes"

	"github.com/dio/luwes/sahl"

	headerauthsahl "github.com/dio/luwes/examples/sahl/header-auth"
)

func main() {
	sdk.RegisterRaw("header-auth-sahl", sahl.Factory(headerauthsahl.Handler))
}
