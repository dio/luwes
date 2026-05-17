# sahl RATIONALE

Why sahl exists and the design decisions behind it.

## Why sahl Exists

luwes gives you zero allocations. It does not give you ergonomics. Writing a
filter against raw luwes requires you to:

- implement `shared.HttpFilter` and `shared.HttpFilterFactory` interfaces
- manage a `sync.Pool` manually for both the handle and any per-request state
- flush header mutations, metadata, and counter increments yourself before
  calling `ContinueRequest` or returning `HeadersStatusStop`
- handle the `OnStreamComplete` / `OnDestroy` lifecycle ordering correctly
  (pool return in `OnDestroy`, not `OnStreamComplete`)
- wire the CGO export in `cmd/main.go`

Here is what a minimal auth filter looks like at the raw layer:

```go
type Filter struct {
    shared.EmptyHttpFilter
    handle  shared.HttpFilterHandle
    factory *Factory
}

func (f *Filter) OnRequestHeaders(headers shared.HeaderMap, _ bool) shared.HeadersStatus {
    var key shared.UnsafeEnvoyBuffer
    if !headers.GetOneInto("x-api-key", &key) || key.Len == 0 {
        f.handle.SendLocalResponse(401, nil, []byte(`{"error":"missing key"}`), "auth")
        return shared.HeadersStatusStop
    }
    f.handle.RequestHeaders().Set("x-user-id", key.ToUnsafeString())
    return shared.HeadersStatusContinue
}

func (f *Filter) OnDestroy() { f.factory.pool.Put(f) }

type Factory struct{ pool sync.Pool }

func (fc *Factory) Create(h shared.HttpFilterHandle) shared.HttpFilter {
    f := fc.pool.Get().(*Filter)
    f.handle = h
    return f
}
func (fc *Factory) OnDestroy() {}
```

That is the floor. Any real filter adds config parsing, metric IDs, metadata
mutations, and response observation on top of it. The boilerplate compounds.

sahl eliminates the boilerplate while preserving the allocation budget as
much as possible. The same filter in sahl:

```go
func Handler(w *sahl.Writer, r *sahl.Request) {
    key, ok := r.Header.Peek("x-api-key")
    if !ok || len(key) == 0 {
        w.Send(http.StatusUnauthorized, `{"error":"missing key"}`)
        return
    }
    w.SetRequestHeader("x-user-id", key)
}
```

sahl handles the pool, the lifecycle callbacks, mutation flushing, and the
`cmd/main.go` wiring pattern. The handler is a plain function.

### The trade-off

sahl pre-copies `r.Method`, `r.Path`, and `r.Host` into Go strings at
`OnRequestHeaders` entry. That costs 3 allocations per request on the CGO
path, unconditionally. Raw luwes with `GetOneInto` costs 0.

| Layer | allocs/request (accept path, CGO) |
|---|---|
| raw luwes (`GetOneInto`) | 0 |
| sahl (`r.Header.Peek`) | 3 |

The 3-alloc floor is not removable without giving up the ergonomic API. It is
the cost of having `r.Method`, `r.Path`, and `r.Host` available as plain Go
strings without the caller managing unsafe lifetimes.

For most filters the 3-alloc floor is fine: the filter is not the bottleneck,
and the simplicity of the handler function is worth more than the 3 allocations.
For zero-alloc hot paths (high-throughput header inspection, passthrough taggers
measured at 50k+ RPS per worker thread), use raw luwes with `GetOneInto`.

### What sahl adds beyond ergonomics

Beyond the handler simplification, sahl adds capabilities that are tedious to
build on raw luwes:

**Body-aware handlers.** `RegisterWithBody` returns `HeadersStatusStopAllAndBuffer`
from `OnRequestHeaders` so Envoy buffers the full request body. The handler fires
at `OnRequestBody(endStream=true)` and receives the complete body via `r.Body()`.
Without sahl, you manage the `StopAllAndBuffer` / `ContinueRequest` state machine
yourself.

**Response observers.** `RegisterWithResponse` gives you a `ResponseHandlerFunc`
called once on response headers (to inspect `Content-Type`) and once per body
chunk. Chunks are always forwarded downstream with `BodyStatusContinue`: zero
added latency, observe-only. Mutations queued in the observer (counter increments,
metadata) are flushed in `OnStreamComplete`. Without sahl, the response-side
lifecycle (headers -> chunks -> stream complete, with the 0-length trailing chunk
edge case) is easy to get wrong.

**Per-listener factory isolation.** `RegisterFactory` is called once per
Envoy filter-chain. Each call returns a new `HandlerFunc` closure that captures
its own parsed config and metric IDs. Two listeners with different `filter_config`
bytes get two independent closures, no shared mutable state. Without sahl,
achieving this requires implementing `HttpFilterConfigFactory` correctly, which
means understanding the three-lifetime model (config-create, filter-create,
request) and the `filterDef` copy semantics.

**Goroutine opt-in.** `Writer.Go` upgrades a request to goroutine mode, runs a
function in a new goroutine, and hops back to the Envoy worker thread via the
scheduler when done. The context passed to `Go` is cancelled on `OnStreamComplete`.
Without sahl, you implement the `Scheduler.Schedule` callback pattern yourself
and handle the cancellation contract.


## Further reading

- [luwes RATIONALE](../RATIONALE.md): why luwes exists, the CGO escape analysis,
  `GetOneInto`, and the handle pool.
- [sahl/README.md](README.md): API reference, registration function comparison
  table, factory design deep-dive.
