# header-auth

An API key authentication filter. Reads `x-api-key` from request headers,
rejects with 401 if absent, injects `x-user-id` with the key value for accepted
requests.

Demonstrates:
- `GetOne` for zero-alloc header reads
- `SendLocalResponse` for early rejection
- `sync.Pool` for filter instance reuse (the pooling pattern for hot-path filters)
- pprof admin server wired via `sdk.StartPprof`

## What it does

- Reads `x-api-key` from request headers using `GetOne` (zero alloc)
- Returns 401 with `{"error":"missing x-api-key"}` if the header is absent
- Injects `x-user-id: <key>` into the request for accepted traffic
- Starts a Go pprof server on `127.0.0.1:6061` (or `LUWES_PPROF_ADDR`)

## Prerequisites

- Go 1.22+ with CGO enabled
- `hey` for load testing: `brew install hey` (optional, only needed for `make flamegraph`)
- Envoy is downloaded automatically to `.bin/envoy` by `make run` / `make flamegraph`

## Make targets

```sh
# Build the .so
make build EXAMPLE=header-auth

# Start Envoy with the filter (foreground, Ctrl-C to stop)
make run EXAMPLE=header-auth

# Capture a pprof flamegraph under load (requires hey)
make flamegraph EXAMPLE=header-auth
```

## Manual steps

**1. Build**

```sh
CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
  -o dist/libheader-auth.so ./examples/header-auth/cmd
```

**2. Run Envoy**

Envoy is downloaded automatically to `.bin/envoy` on first run:

```sh
make run EXAMPLE=header-auth
```

Or manually:

```sh
GODEBUG=cgocheck=0 \
ENVOY_DYNAMIC_MODULES_SEARCH_PATH=$(pwd)/dist \
.bin/envoy -c examples/header-auth/envoy.yaml --log-level warning
```

**3. Test**

In a separate terminal:

```sh
# Check Envoy is ready
curl http://127.0.0.1:9901/ready

# Request without key -- expect 401
curl -si http://localhost:10000/
```

Expected:
```
HTTP/1.1 401 Unauthorized
{"error":"missing x-api-key"}
```

```sh
# Request with key -- expect 200
curl -si -H "x-api-key: my-token" http://localhost:10000/
```

Expected:
```
HTTP/1.1 200 OK
ok
```

The filter injects `x-user-id: my-token` into the upstream request. With
`direct_response` there is no upstream to observe it, but a real upstream would
receive the header.

**4. Load test**

```sh
# Warm up
hey -n 10000 -c 50 -H "x-api-key: bench" http://localhost:10000/

# Sustained load
hey -n 500000 -c 100 -H "x-api-key: bench" http://localhost:10000/
```

**5. pprof profile**

After at least one request (which triggers `configFactory.Create` and starts
the pprof server):

```sh
# Check pprof is up
curl http://127.0.0.1:6061/healthz

# Capture allocs profile under load
hey -n 500000 -c 200 -H "x-api-key: bench" http://localhost:10000/ &
sleep 2
curl http://127.0.0.1:6061/debug/pprof/allocs -o /tmp/allocs.out

# View top allocations
go tool pprof -alloc_objects -top /tmp/allocs.out

# Open flamegraph in browser
go tool pprof -alloc_objects -http=:8080 /tmp/allocs.out
```

Override the pprof address:

```sh
LUWES_PPROF_ADDR=127.0.0.1:7070 \
GODEBUG=cgocheck=0 \
ENVOY_DYNAMIC_MODULES_SEARCH_PATH=$(pwd)/dist \
.bin/envoy -c examples/header-auth/envoy.yaml --log-level warning
```

## Filter structure

```
examples/header-auth/
  header_auth.go   -- Filter, Factory with sync.Pool, OnRequestHeaders
  cmd/main.go      -- Wiring: Register, StartPprof, RegisterHttpFilterConfigFactories
  envoy.yaml       -- Minimal Envoy config: listener + direct_response
```

## Key patterns

**sync.Pool for filter instances.** The factory holds a `sync.Pool` of `*Filter`
instances. `Create` gets from the pool; `OnStreamComplete` returns to the pool.
This eliminates the per-request `*Filter` heap allocation on the hot path.

**GetOne, not Get.** `GetOne` returns a value type (`UnsafeEnvoyBuffer`) with zero
allocation. `Get` returns a `[]UnsafeEnvoyBuffer` slice -- always allocates. For
single-value headers use `GetOne`.

**ToUnsafeString for immediate use.** `key.ToUnsafeString()` returns a string
backed by Envoy memory. Valid only during the current callback. Used here for the
`SetHeader` call which happens in the same callback -- safe. Do not store it.

## Benchmark results (Apple M1, Envoy 1.38.0)

From `make flamegraph EXAMPLE=header-auth` -- 500k requests under concurrency 200:

```
Type: alloc_objects

      flat  flat%   sum%
  491527  98.90%    getSingleHeader  (CGO boundary: valueView escapes)
    4682   0.94%    newDymStreamPluginHandle (pool misses -- GC cleared sync.Pool)
```

The filter pool is working: only 4682 handle allocs over 500k requests (pool
misses from GC cycles) vs 500k without the pool. The dominant cost is the CGO
boundary allocation in `getSingleHeader` -- structural, present in both upstream
SDK and luwes, not reducible at the Go layer.
