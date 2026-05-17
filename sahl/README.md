# sahl

`sahl` (سهل, Arabic for "easy") is an ergonomic HTTP filter API for Envoy dynamic
modules built on top of luwes. It gives you familiar Go types ([Request], [Writer],
[Header]) without the goroutine-per-request overhead of jisr.

Handlers run on the Envoy worker thread by default. Blocking work is opt-in via
`Writer.Go`.

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

### Per-config state with RegisterFactory

When a filter needs metric IDs, parsed config, or per-factory pools:

```go
func init() {
    sahl.RegisterFactory("my-filter",
        func(h sahl.ConfigHandle) (sahl.HandlerFunc, error) {
            counter, err := h.DefineCounter("my_requests_total", "result")
            if err != nil {
                return nil, err
            }
            cfg, err := parseConfig(h.RawConfig())
            if err != nil {
                return nil, err
            }
            return func(w *sahl.Writer, r *sahl.Request) {
                // counter and cfg are captured once, shared across requests.
                w.IncrementCounter(counter, 1, "ok")
            }, nil
        },
    )
}
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
|-------|------------------------------|
| raw luwes (GetOneInto) | 0 |
| sahl (Peek) | 3 |
| jisr (goroutine per request) | 20+ |

## Example: header-auth-sahl

`examples/header-auth-sahl` reimplements `examples/header-auth` using sahl.
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
