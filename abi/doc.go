// Package abi contains the vendored C ABI header for Envoy dynamic modules.
//
// This file exists so that [go mod vendor] includes this directory.
// Without a .go file, the Go toolchain skips directories during vendoring,
// which causes abi.h (needed by abi_impl's CGO include) to be missing.
//
// Do not add any Go code here. The only content that matters is abi.h.
// Pin and update abi.h via scripts/sync-abi.sh.
package abi
