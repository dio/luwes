# luwes

[![ci](https://github.com/dio/luwes/actions/workflows/ci.yml/badge.svg)](https://github.com/dio/luwes/actions/workflows/ci.yml)
[![Coverage Status](https://coveralls.io/repos/github/dio/luwes/badge.svg?branch=main)](https://coveralls.io/github/dio/luwes?branch=main)

> **On the coverage number:** the badge only counts packages testable without
> a live Envoy process. `abi_impl` (the CGO layer that backs every header,
> body, span, and scheduler call) can't run without the Envoy ABI loaded, so
> it's excluded from unit tests and pulls down the total. That code gets
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

## Performance Report

Measured against Envoy 1.38.0, ABI v0.1.0, Apple M1 (Go 1.26, `-race` off).
Load test: `hey -n 1_000_000 -c 200` against the header-auth filter.

### Upstream SDK baseline

```
BenchmarkHeaderAuthAccept   96 B/op   1 allocs/op   ~118 ns/op
```

One allocation per request on every worker thread. At 50k RPS that is
50k heap allocations per second, per thread. The flamegraph showed a single
source: `getSingleHeader` (98.90% of all allocations), caused by `&valueView`
crossing the CGO boundary and escaping to the heap.

### luwes: handle pool (Phase 2)

```
BenchmarkHeaderAuthAccept   0 B/op   0 allocs/op   ~64 ns/op
```

`dymHttpFilterHandle` structs are pooled via `sync.Pool`. Pool return is in
`on_http_filter_destroy` (the guaranteed-last callback), not in
`stream_complete`, which can race with `destroy`. The flamegraph collapses
handle allocation to 0.94% (GC evictions only).

### luwes: GetOneInto (Phase 5)

`GetOneInto(key string, out *UnsafeEnvoyBuffer) bool` eliminates the
remaining CGO escape. The caller stack-allocates `out`; it is cast to
`*C.envoy_dynamic_module_type_envoy_buffer` via `unsafe.Pointer`. The
cast is safe: both structs are 16 bytes, `ptr` at offset 0, `length` at
offset 8.

```
BenchmarkGetOneInto   0 B/op   0 allocs/op   ~17 ns/op
```

The 98.90% in the flamegraph is gone. Usage:

```go
func (f *Filter) OnRequestHeaders(headers shared.HeaderMap, _ bool) shared.HeadersStatus {
    var key shared.UnsafeEnvoyBuffer
    if !headers.GetOneInto("x-api-key", &key) {
        f.handle.SendLocalResponse(401, nil, []byte(`{"error":"missing x-api-key"}`), "auth")
        return shared.HeadersStatusStop
    }
    f.handle.RequestHeaders().Set("x-user-id", key.ToUnsafeString())
    return shared.HeadersStatusContinue
}
```

### Flamegraphs

Generated via `make flamegraph` (go-torch + brendangregg/FlameGraph). Width
proportional to allocation count. Hover to see stack frames and counts.

**Before (upstream SDK)**

`getSingleHeader` owns a wide bar at 98.90%. Every `GetOne` call on a hit forces
`var valueView C.envoy_dynamic_module_type_envoy_buffer` onto the heap
because its address crosses the CGO boundary. The Go runtime cannot prove C
won't store the pointer, so it pins it on the heap. There is no way around
this with the current return-by-value API.

![baseline flamegraph](bench/profiles/flamegraph_baseline.svg)

**After (luwes + GetOneInto)**

`getSingleHeader` is gone from the top. `GetOneInto` hands the C function a
pointer to a caller-owned buffer. The caller declares `var key shared.UnsafeEnvoyBuffer`,
which the compiler stack-allocates (its address stays in Go-managed space and
never escapes). The cast to `*C.envoy_dynamic_module_type_envoy_buffer` via
`unsafe.Pointer` is valid because both structs share the same layout: 16 bytes,
`ptr` at offset 0, `length` at offset 8.

What remains (the thin bars) is `(*Filter).OnRequestHeaders`: the
`RequestHeaders().Set("x-user-id", ...)` call, which allocates a new header
string. That is expected and unavoidable without a mutation API that writes
directly into Envoy's header table.

![after getoneinto flamegraph](bench/profiles/flamegraph_getoneinto.svg)

### Alloc benchmark summary

| Benchmark | upstream SDK | luwes |
|-----------|-------------|-------|
| HeaderAuthAccept | 1 alloc/op | **0 allocs/op** |
| GetOne (hit) | 1 alloc/op | 1 alloc/op |
| GetOneInto (hit) | n/a | **0 allocs/op** |
| GetAll (10 headers) | 2 allocs/op | 1 alloc/op |

`GetOne` allocates on the real CGO path in both SDKs: `&valueView` escapes to the
heap at the CGO boundary regardless. `GetOneInto` eliminates it by letting the caller
own the buffer. The fake benchmark shows 0 for `GetOne` in luwes because there is no
CGO boundary on the fake path; the flamegraph is the ground truth.

## ABI

Vendored at `abi/abi.h` (pinned commit in `abi/VERSION`). Run `make sync-abi COMMIT=<hash>`
to update. An automated weekly check opens a PR when `envoy/main` drifts.
