# sahl

`sahl` (سهل, Arabic for "easy") is an ergonomic HTTP filter API for Envoy dynamic
modules built on top of luwes.

- `Request` gives you the incoming request: method, path, host, and headers via
  `r.Header.Peek` (zero-alloc unsafe string) or `r.Header.Get` (copied, cacheable).
- `Writer` queues mutations to apply after your handler returns: set request headers,
  send a local response, set dynamic metadata, increment counters, clear route cache.
  Blocking work is opt-in via `Writer.Go`, which runs a goroutine and hops back to
  the Envoy worker thread when done.
- `Header` is the header accessor on `Request`: `Peek`, `Get`, `Values`, `Range`.

Handlers run on the Envoy worker thread synchronously by default. No goroutine is
spawned unless your handler calls `Writer.Go`.

## Quick start

```go
package myfilter

import (
    "net/http"

    "github.com/dio/luwes/sahl"
)

func init() {
    sahl.Register("my-filter", func(w *sahl.Writer, r *sahl.Request) {
        key, ok := r.Header.Peek("x-api-key")
        if !ok || len(key) == 0 {
            w.Send(http.StatusUnauthorized, `{"error":"missing key"}`)
            return
        }
        w.SetRequestHeader("x-user-id", key)
    })
}
```

Wire it in `cmd/main.go`:

```go
package main

import (
    sdk  "github.com/dio/luwes"
    _    "github.com/dio/luwes/abi_impl"
    _    "your/filter/package"
)

func init() { sdk.RegisterHttpFilterConfigFactories(sahl.Factories()) }
func main() {}
```

Build:

```sh
CGO_ENABLED=1 go build -trimpath -buildmode=c-shared -o dist/libmyfilter.so ./cmd
```

## API

### Header reads

| Method | Allocs (CGO) | When to use |
|--------|-------------|-------------|
| `r.Header.Peek(key)` | 0 | Hot path: unsafe string, valid within callback only |
| `r.Header.Get(key)` | 1 (first call), 0 (cached) | When you need the value after the handler returns |
| `r.Method`, `r.Path`, `r.Host` | 0 per read (pre-copied) | Pseudo-headers, always available |

`Peek` returns an unsafe string pointing into Envoy-owned memory. Valid only
during the current handler call. Never store it past the handler return.

`Get` copies the value into Go memory on the first call, caches it, and returns
the same Go string on repeat calls. Safe to store past the handler.

### Mutations

Mutations are queued and applied on the Envoy worker thread after the handler
returns (or when `Writer.Go`'s goroutine finishes). No synchronization needed.

```go
w.SetRequestHeader("x-key", "value") // set or overwrite request header
w.SetMetadata("ns", "key", "value")  // stream dynamic metadata
w.ClearRouteCache()                  // re-evaluate cluster selection
w.IncrementCounter(id, 1, "tag-val") // metric counter
w.RecordHistogram(id, latencyMs)     // metric histogram
```

### Local responses

```go
w.Send(http.StatusUnauthorized, `{"error":"missing key"}`) // string body
w.SendBytes(http.StatusForbidden, bodyBytes)               // byte slice body
```

`Send`/`SendBytes` can be called at most once per request. A second call is a
no-op. The filter chain is stopped after a local response.

### Blocking work with Writer.Go

For handlers that need to make external calls (Redis, auth service, DB):

```go
func myHandler(w *sahl.Writer, r *sahl.Request) {
    key, ok := r.Header.Peek("x-api-key")
    if !ok {
        w.Send(http.StatusUnauthorized, `{"error":"missing key"}`)
        return
    }

    // Worker thread is released here. Envoy processes other requests.
    w.Go(func(ctx context.Context) {
        session, err := redis.Get(ctx, key)
        if err != nil {
            w.Send(http.StatusUnauthorized, `{"error":"invalid key"}`)
            return
        }
        w.SetRequestHeader("x-user-id", session)
        // Mutations are applied and ContinueRequest is called when this returns.
    })
}
```

`Go` must be called at most once per request. Panics on duplicate calls.
The context passed to `Go` is cancelled if the client disconnects.

### Per-config state: Factories

When a filter needs metric IDs, parsed config, or per-listener isolation, use
`RegisterFactory`. The factory is called once per Envoy filter-chain (listener);
each call returns a new `HandlerFunc` that closes over its own state.

#### The three lifetimes

| Lifetime | Created | Type in sahl |
|---|---|---|
| program init | once per process | global registry entry |
| filter config create | once per listener / filter chain | `configFactory` -> `filterFactory` |
| filter instance create | once per HTTP request | `sahlFilter` (pool-allocated) |

#### Registration function comparison

| Function | Per-listener state | Metrics | Body buffering | Response observer | Multi-listener safe |
|---|---|---|---|---|---|
| `Register` | no | no | no | no | yes |
| `RegisterWithConfig` | no | yes | no | no | **NO** (package vars overwritten) |
| `RegisterWithResponse` | no | no | no | yes | yes |
| `RegisterWithConfigAndResponse` | no | yes | no | yes | **NO** |
| `RegisterWithBody` | no | no | yes | no | yes |
| `RegisterWithBodyAndResponse` | no | no | yes | yes | yes |
| `RegisterWithBodyConfigAndResponse` | no | yes | yes | yes | **NO** |
| `RegisterFactory` | **yes** | **yes** | no | no | **yes** |

"Multi-listener safe" means two envoy.yaml listeners can reference this filter
with different `filter_config` bytes without one overwriting the other's state.
Any `Register*Config*` function that writes to package-level vars is not safe
for multi-listener use.

#### Side-by-side: RegisterWithConfig vs RegisterFactory

Both define a counter and parse config. The difference is where state lives.

**RegisterWithConfig:** state in package vars, second listener silently overwrites first.

```go
// package-level: shared by ALL listeners using this filter
var (
    reqTotal sahl.MetricID
    allowed  map[string]struct{}
)

func init() {
    sahl.RegisterWithConfig("auth",
        func(h sahl.ConfigHandle) error {
            cfg := parseConfig(h.RawConfig()) // listener 2 overwrites listener 1
            allowed = buildSet(cfg.AllowedKeys)
            reqTotal, _ = h.DefineCounter("auth_requests_total", "result")
            return nil
        },
        func(w *sahl.Writer, r *sahl.Request) {
            key, _ := r.Header.Peek("x-api-key")
            if _, ok := allowed[key]; !ok { // reads the LAST-written allowed
                w.IncrementCounter(reqTotal, 1, "rejected")
                w.Send(401, `{"error":"unauthorized"}`)
                return
            }
            w.IncrementCounter(reqTotal, 1, "allowed")
        },
    )
}
```

**RegisterFactory:** state in closure, each listener gets its own copy.

```go
func init() {
    sahl.RegisterFactory("auth",
        func(h sahl.ConfigHandle) (sahl.HandlerFunc, error) {
            // runs once per listener; vars are local to this call
            cfg := parseConfig(h.RawConfig())
            allowed := buildSet(cfg.AllowedKeys) // each listener gets its own
            reqTotal, err := h.DefineCounter("auth_requests_total", "result")
            if err != nil {
                return nil, err
            }
            return func(w *sahl.Writer, r *sahl.Request) {
                key, _ := r.Header.Peek("x-api-key")
                if _, ok := allowed[key]; !ok { // reads THIS listener's allowed
                    w.IncrementCounter(reqTotal, 1, "rejected")
                    w.Send(401, `{"error":"unauthorized"}`)
                    return
                }
                w.IncrementCounter(reqTotal, 1, "allowed")
            }, nil
        },
    )
}
```

See `sahl/examples/auth` for a complete two-listener isolation example.

#### The metric ID constraint

Metric IDs must be defined at filter config time, not per-request. `ConfigHandle`
is only available in `configFn` or the factory function; you cannot call
`DefineCounter` from a `HandlerFunc`. Envoy allocates the metric slot once.

#### configHandleImpl wrapping in tests

`configFactory.Create` wraps the raw config bytes passed as its second parameter.
`RawConfig()` returns that parameter, not the fake handle's field. In tests:

```go
// correct: raw bytes flow through the second parameter
factory, err := def.Create(&fakeHandle{}, []byte(configJSON))

// wrong: RawConfig() returns nil, config parse gets empty struct
factory, err := def.Create(&fakeHandle{raw: configJSON}, nil)
```

### Middleware

```go
func authMiddleware(next sahl.HandlerFunc) sahl.HandlerFunc {
    return func(w *sahl.Writer, r *sahl.Request) {
        if r.Header.Get("authorization") == "" {
            w.Send(http.StatusUnauthorized, `{"error":"missing auth"}`)
            return
        }
        next(w, r)
    }
}

sahl.Register("my-filter", sahl.Chain(myHandler, authMiddleware, loggingMiddleware))
```

`Chain(h, mw1, mw2, ...)`: `mw1` is outermost (runs first, can call `next` or short-circuit).

## Multiple filters in one .so

A single package can register several filters in one `init()`. Each is keyed by
name and must match the `filter_name` in envoy.yaml.

```go
func init() {
    sahl.Register("spa", SPAHandler)
    sahl.Register("api-backend", sahl.Chain(APIHandler, apiLogMiddleware))
}
```

Both filters share the same process and the same embedded assets (e.g.
`//go:embed ui/dist`), but have separate handler functions, separate per-request
pools, and separate metric namespaces. Envoy dispatches via `filter_name`:

```yaml
http_filters:
  - name: api-backend
    typed_config:
      "@type": type.googleapis.com/envoy.extensions.filters.http.dynamic_modules.v3.DynamicModuleFilter
      dynamic_module_config: { name: myfilter }
      filter_name: api-backend   # must match Register() call
  - name: spa
    typed_config:
      "@type": type.googleapis.com/envoy.extensions.filters.http.dynamic_modules.v3.DynamicModuleFilter
      dynamic_module_config: { name: myfilter }
      filter_name: spa           # must match Register() call
```

Registering the same name twice panics at startup: `BUG: sahl filter %q registered twice`.

## Allocation budget

All numbers below are for the real CGO path (live Envoy). The fake/pure-Go path
used by `go test ./bench/...` shows higher counts due to interface dispatch; see
`bench/sahl_bench_test.go` for the per-line pprof breakdown.

| Path | allocs/op | breakdown |
|------|-----------|-----------|
| NoOp (pool warm) | 3 | Method + Path + Host pre-copy; unavoidable |
| Accept, Peek hot path | 3 | same; Peek adds 0 |
| Accept, first Get | 4 | +1 for ToString into cache |
| Accept, cached Get | 3 | cache hit; 0 additional |
| Reject, w.Send | 4+ | SendLocalResponse body copy; unavoidable |

The 3-alloc floor is the cost of pre-copying `r.Method`, `r.Path`, and `r.Host`
into Go memory at callback entry. That is the ergonomic trade-off: handlers read
them as plain Go strings without managing Envoy memory lifetimes. Use raw luwes
with `GetOneInto` if you need 0 allocs end-to-end.

Comparison:

| Layer | allocs/op (CGO, accept path) |
|---|---|
| raw luwes (`GetOneInto`) | 0 |
| sahl (`Peek`) | 3 |

## Example: header-auth-sahl

`sahl/examples/header-auth` reimplements `examples/header-auth` using sahl.
Compare them:

**Raw luwes (header-auth):**
```go
type Filter struct {
    shared.EmptyHttpFilter
    handle  shared.HttpFilterHandle
    factory *Factory
}
func (f *Filter) OnRequestHeaders(headers shared.HeaderMap, _ bool) shared.HeadersStatus {
    var key shared.UnsafeEnvoyBuffer
    if !headers.GetOneInto("x-api-key", &key) || key.Len == 0 {
        f.handle.SendLocalResponse(401, nil, []byte(`{"error":"missing x-api-key"}`), "auth")
        return shared.HeadersStatusStop
    }
    f.handle.RequestHeaders().Set("x-user-id", key.ToUnsafeString())
    return shared.HeadersStatusContinue
}
// + Factory with sync.Pool, Create, OnDestroy, OnStreamComplete ...
```

**sahl (header-auth-sahl):**
```go
func Handler(w *sahl.Writer, r *sahl.Request) {
    key, ok := r.Header.Peek("x-api-key")
    if !ok || len(key) == 0 {
        w.Send(http.StatusUnauthorized, `{"error":"missing x-api-key"}`)
        return
    }
    w.SetRequestHeader("x-user-id", key)
}
```

sahl handles pool management, lifecycle callbacks, and mutation flushing.
The trade-off: 3 fixed allocs per request vs 0 with raw luwes.
