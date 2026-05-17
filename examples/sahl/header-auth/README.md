# header-auth-sahl

The sahl port of `examples/header-auth`. Same API key authentication logic,
implemented with [sahl.Register] instead of the raw luwes SDK.

Compare the two implementations:

**Raw luwes (examples/header-auth):**
```go
type Filter struct {
    shared.EmptyHttpFilter
    handle  shared.HttpFilterHandle
    factory *Factory
}
type Factory struct { counter shared.MetricID; pool sync.Pool }

func (f *Filter) OnRequestHeaders(headers shared.HeaderMap, _ bool) shared.HeadersStatus {
    var key shared.UnsafeEnvoyBuffer
    if !headers.GetOneInto("x-api-key", &key) || key.Len == 0 {
        f.handle.SendLocalResponse(401, nil, []byte(`{"error":"missing x-api-key"}`), "auth")
        return shared.HeadersStatusStop
    }
    f.handle.RequestHeaders().Set("x-user-id", key.ToUnsafeString())
    return shared.HeadersStatusContinue
}
// + OnStreamComplete, OnDestroy, Create, OnDestroy on factory ...
```

**sahl (this example):**
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

## What it does

- Reads `x-api-key` from request headers using `r.Header.Peek` (zero-alloc on CGO path)
- Rejects with 401 if absent or empty
- Injects `x-user-id: <key>` into the upstream request

## Allocation analysis

All numbers below are for the real CGO path (live Envoy).

| Path | allocs/op | breakdown |
|------|-----------|-----------|
| Accept (Peek hit) | 3 | Method + Path + Host pre-copy |
| Reject (Send) | 4+ | 3 pre-copies + SendLocalResponse body |

Compare to raw luwes header-auth: 0 allocs/op on the accept path (no pre-copies,
GetOneInto is zero-alloc). The 3-alloc floor is the cost of sahl's Method/Path/Host
pre-copy convenience.

## Build

```sh
make build EXAMPLE=header-auth-sahl
# or directly:
CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
  -o dist/libheader-auth-sahl.so ./examples/sahl/header-auth/cmd
```

## Run

```sh
make run EXAMPLE=header-auth-sahl
```

## Test

```sh
# Request without key (expect 401)
curl -si http://localhost:10000/

# Request with key (expect 200)
curl -si -H "x-api-key: my-token" http://localhost:10000/
```

## Filter structure

```
examples/sahl/header-auth/
  header_auth_sahl.go   Handler function (no pool, no struct, no lifecycle hooks)
  cmd/main.go           Wiring: sdk.RegisterRaw with sahl.Factory
  envoy.yaml            Minimal Envoy config: listener + direct_response
  README.md             This file
```
