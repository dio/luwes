# luwes

[![ci](https://github.com/dio/luwes/actions/workflows/ci.yml/badge.svg)](https://github.com/dio/luwes/actions/workflows/ci.yml)
[![Coverage Status](https://coveralls.io/repos/github/dio/luwes/badge.svg?branch=main)](https://coveralls.io/github/dio/luwes?branch=main)

> **On the coverage number:** the badge only counts packages testable without
> a live Envoy process. `abi_impl` -- the CGO layer that backs every header,
> body, span, and scheduler call -- can't run without the Envoy ABI loaded, so
> it's excluded from unit tests and pulled down the total. That code gets
> exercised by the e2e suite against a real Envoy binary in CI.
> Unit-testable package breakdown: hello 100%, utility 69%, header-auth 67%,
> shared/fake 32%, root registry 26%.

Zero-allocation Go SDK for Envoy dynamic modules. Drop-in replacement for
`github.com/envoyproxy/envoy/source/extensions/dynamic_modules/sdk/go`.

See [RATIONALE.md](RATIONALE.md) for why this exists.

## Install

```
go get github.com/dio/luwes
```

Requires Envoy built with dynamic module support (ABI v0.1.0, Envoy >= 1.38.0).

## Usage

```go
// cmd/main.go
package main

import (
    sdk "github.com/dio/luwes"
    _   "github.com/dio/luwes/abi_impl"
    _   "your/filter/package" // calls sdk.Register in its init()
)

func init() {
    sdk.RegisterHttpFilterConfigFactories(sdk.Factories())
}

func main() {}
```

```
CGO_ENABLED=1 go build -trimpath -buildmode=c-shared -o dist/libmyfilter.so ./cmd
```

## Examples

| Example | What it shows |
|---------|--------------|
| [hello](examples/hello/) | Minimal filter: read `:path`, stamp response header |
| [header-auth](examples/header-auth/) | API key auth, sync.Pool, 0 allocs/op on hot path |
| [observability](examples/observability/) | Metrics, tracing, structured logging |

Each example has an `envoy.yaml` and a `README.md` with run instructions.

## Development

```
make build          # build .so for host (dev)
make run            # build + start Envoy (requires .bin/envoy)
make test           # go test -race ./...
make coverage       # coverage report
make lint           # golangci-lint
make bench          # alloc benchmarks
make flamegraph     # pprof allocs under load (requires hey)
make e2e            # integration tests against real Envoy
```

## Performance

After the handle pool and `GetOne` nil-miss optimization:

```
BenchmarkHeaderAuthAccept   0 B/op   0 allocs/op   ~64 ns/op
BenchmarkGetOne             0 B/op   0 allocs/op   ~17 ns/op
BenchmarkGetMiss            0 B/op   0 allocs/op   ~19 ns/op
```

The remaining allocation source (`getSingleHeader`) is structural -- a CGO
boundary escape that requires an ABI change to eliminate. See RATIONALE.md.

## ABI

Vendored at `abi/abi.h` (pinned commit in `abi/VERSION`). Run `make sync-abi COMMIT=<hash>`
to update. An automated weekly check opens a PR when `envoy/main` drifts.
