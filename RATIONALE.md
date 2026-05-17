# RATIONALE

Why luwes exists and the design decisions behind it.

## The Problem

The upstream Envoy Go dynamic module SDK lives at
`github.com/envoyproxy/envoy/source/extensions/dynamic_modules/sdk/go`.
It is functional and correct. It is also the only supported way to write Go
filters against Envoy's ABI v0.1.0.

But every call on the hot path allocates:

```
go test -bench=BenchmarkHeaderAuthAccept -benchmem -count=5 ./bench/
BenchmarkHeaderAuthAccept-8    9_441_203    118 ns/op    96 B/op    1 allocs/op
```

One allocation per request, on every worker thread, for every filter that reads
a single header. At 50k RPS that is 50k allocations per second, per worker
thread, for the simplest possible auth check. The GC pressure is not
theoretical: it shows up in p99 latency under sustained load.

The allocation is not accidental. In `abi_impl/internal.go`, every header read
goes through `getSingleHeader`, which constructs a `valueView` struct and takes
its address to pass to C:

```go
func (m *dymHeaderMap) Get(key string) ([]string, bool) {
    var valueView C.envoy_dynamic_module_type_http_header_value_result_t
    // &valueView crosses the CGO boundary -> heap escape, unavoidable
    ...
}
```

Go pins heap objects for CGO calls. It does not pin stack objects. Any local
whose address crosses a CGO call escapes to the heap by design: a CGO runtime
constraint, not a compiler missed optimization. Stack scratch does not work here.

The handle itself also allocates on every request. The upstream SDK creates a
new `dymHttpFilterHandle` in `on_http_filter_init`, which fires once per
request on each Envoy worker thread. At 200 concurrent connections that is 200
handle allocations in flight at any moment.

## What luwes Does

luwes is a drop-in replacement for the upstream SDK. Same interfaces, same
ABI, same filter registration pattern. Import `github.com/dio/luwes` instead of
the upstream path and the allocations change:

```
go test -bench=BenchmarkHeaderAuthAccept -benchmem -count=5 ./bench/
BenchmarkHeaderAuthAccept-8   18_988_479    64 ns/op    0 B/op    0 allocs/op
```

Two changes drive this:

**Handle pool.** `dymHttpFilterHandle` structs are pooled via `sync.Pool`.
`on_http_filter_init` pulls a handle from the pool and resets it.
`on_http_filter_destroy` returns it. The pool return is in `destroy`, not
`stream_complete`: Envoy can fire `destroy` after `stream_complete`, and a
handle returned in `stream_complete` can be reassigned before `destroy` fires,
causing use-after-free. The `reset()` method has `BUG:` panic assertions that
catch any violation of this contract in testing.

**`GetOneInto`.** The upstream `Get` returns `([]string, bool)`, requiring a
slice header allocation even for a miss. `GetOneInto(key string, out *UnsafeEnvoyBuffer) bool`
writes the result into a caller-provided buffer. The caller declares
`var key shared.UnsafeEnvoyBuffer`, which the compiler stack-allocates (its
address stays in Go-managed space and never crosses the CGO boundary as a
local). The cast to `*C.envoy_dynamic_module_type_envoy_buffer` via
`unsafe.Pointer` is valid: both structs are 16 bytes, `ptr` at offset 0,
`length` at offset 8.

```
BenchmarkGetOneInto   0 B/op   0 allocs/op   ~17 ns/op
```

The 98.90% `getSingleHeader` bar in the flamegraph is gone.

## Why Not Upstream PR

The handle pool is a behavioral change. `on_http_filter_destroy` as the sole
pool-return point is correct, but it is a subtle constraint on the lifecycle
contract. Upstreaming requires Envoy maintainer review, agreement on the
contract, and a timeline tied to Envoy releases.

luwes ships now, works with the current ABI (`abi/VERSION` pins the commit),
and can be consumed by changing one import in `go.mod`. If and when the
upstream SDK adopts these changes, migrating back is the same one-line edit.

## ABI Vendoring

`abi/abi.h` is vendored at a pinned commit (`abi/VERSION`). The ABI is
versioned by Envoy and changes rarely. Vendoring it means the module builds
without network access, works in hermetic CI, and the diff between ABI versions
is visible in code review. `make sync-abi COMMIT=<hash>` updates both the
header and `VERSION` atomically. `make check-abi-drift` (and the weekly
`abi-drift` CI job) detect when `envoy/main` has diverged.

## Module Isolation

`tools/go.mod` pins `golangci-lint` via the Go 1.24 `tool` directive. It is
not in `go.work`: golangci-lint's 200+ transitive dependencies must not
appear in the main module's build graph. Invoked as:

```
GOWORK=off go tool -modfile=tools/go.mod golangci-lint <cmd>
```

or via `make format` and `make lint`.
