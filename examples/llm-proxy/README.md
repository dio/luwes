# llm-proxy

Zero-allocation LLM proxy filter built on raw luwes (no sahl layer).

Routes requests to OpenAI, Anthropic, or a default cluster based on the `model`
field in the JSON request body. Taps SSE response streams for token usage without
buffering the full body.

Demonstrates:
- `HeadersStatusStopAllAndBuffer` + `OnRequestBody` for body inspection before routing
- `gjson.GetBytes` for JSON field extraction without a bespoke scanner
- Static prefix routing table: no map lookup, no allocation per request
- `cluster_header` route: model routing without xDS updates
- HeadTail ring buffer for SSE tap: first 8 KB + last 64 KB, middle never stored
- `sync.Pool` for filter instance and ring buffer reuse

## What it does

**Request phase:**

1. Returns `HeadersStatusStopAllAndBuffer` to hold the request until the body arrives.
2. Scans the body for `"model": "<value>"` via `scanModel` (zero-alloc byte scan, returns
   a slice into Envoy's buffer).
3. Maps model to cluster via a static prefix table: `gpt-*` and `o1`/`o3` to `openai`,
   `claude-*` to `anthropic`, anything else to `default`.
4. Sets `x-cluster` and calls `ClearRouteCache` so Envoy's `cluster_header` route picks
   the right upstream.
5. Increments `llm_proxy_requests_total{cluster}`.

**Response phase:**

1. Checks `Content-Type: text/event-stream`. Non-SSE responses pass through untouched.
2. Writes each chunk into a HeadTail ring (8 KB head + 64 KB tail). `BodyStatusContinue`
   on every chunk: zero added latency, data flows downstream while the ring fills.
3. On `endStream=true`, scans head and tail for token usage in OpenAI and Anthropic SSE
   wire formats. The middle of the stream is never stored.
4. Emits `llm_proxy_input_tokens{cluster}` and `llm_proxy_output_tokens{cluster}`.

## Prerequisites

- Go 1.22+ with CGO enabled
- Envoy is downloaded automatically to `.bin/envoy` by `make run`
- `hey` for load testing: `brew install hey` (optional, only needed for `make flamegraph`)

## Make targets

From this directory:

```sh
make build   # compile libllm-proxy.so
make run     # build + start Envoy (foreground, Ctrl-C to stop)
make test    # unit tests, no Envoy required
make clean   # remove built .so
```

From the repo root (also supports flamegraph and cross-compile):

```sh
make build              EXAMPLE=llm-proxy
make run                EXAMPLE=llm-proxy
make flamegraph         EXAMPLE=llm-proxy
make build-linux-amd64  EXAMPLE=llm-proxy
```

## Manual steps

**1. Build**

```sh
CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
  -o dist/libllm-proxy.so ./examples/llm-proxy/cmd
```

**2. Run Envoy**

Envoy is downloaded automatically to `.bin/envoy` on first run:

```sh
make run EXAMPLE=llm-proxy
```

Or manually:

```sh
GODEBUG=cgocheck=0 \
ENVOY_DYNAMIC_MODULES_SEARCH_PATH=$(pwd)/dist \
.bin/envoy -c examples/llm-proxy/envoy.yaml --log-level warning
```

**3. Send requests**

In a separate terminal:

```sh
# Check Envoy is ready
curl http://127.0.0.1:9901/ready

# Route to openai cluster
curl -s http://localhost:10000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}'

# Route to anthropic cluster
curl -s http://localhost:10000/v1/messages \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-3-sonnet","messages":[{"role":"user","content":"hello"}]}'

# Route to default cluster (unknown model)
curl -s http://localhost:10000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini-pro","messages":[{"role":"user","content":"hello"}]}'
```

**4. Check metrics**

```sh
curl -s http://localhost:9901/stats | grep llm_proxy
```

Expected counters after routing:

```
llm_proxy.llm_proxy_requests_total.openai: 1
llm_proxy.llm_proxy_input_tokens.openai: <N>
llm_proxy.llm_proxy_output_tokens.openai: <N>
```

**5. Load test**

```sh
hey -n 100000 -c 100 \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}' \
  -m POST \
  http://localhost:10000/v1/chat/completions
```

**6. pprof profile**

After at least one request (which triggers `NewFactory` and starts the pprof server):

```sh
# Capture allocs profile under load
hey -n 500000 -c 200 \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[]}' \
  -m POST \
  http://localhost:10000/v1/chat/completions &
sleep 2
curl http://127.0.0.1:6061/debug/pprof/allocs -o /tmp/allocs.out

# View top allocations
go tool pprof -alloc_objects -top /tmp/allocs.out

# Open flamegraph in browser
go tool pprof -alloc_objects -http=:8080 /tmp/allocs.out
```

## Cluster routing table

Edit `scan.go` to add or reorder prefixes. No map, static array, first match wins:

```go
var clusterRoutes = [...]routeEntry{
    {[]byte("gpt-"),    "openai"},
    {[]byte("o1"),      "openai"},
    {[]byte("o3"),      "openai"},
    {[]byte("claude-"), "anthropic"},
}
```

`resolveCluster` converts the model string to `[]byte` once (stack-allocated for
strings under ~32 bytes) then walks the array with `bytes.HasPrefix`. No heap.

## Filter structure

```
examples/llm-proxy/
  llm_proxy.go      Filter, Factory with sync.Pool, OnRequest*/OnResponse* callbacks
  scan.go           scanModel (zero-alloc JSON scan), resolveCluster, routing table
  sse.go            extractUsage: OpenAI + Anthropic SSE token extraction
  llm_proxy_test.go filter integration tests + BenchmarkLLMProxy_ModelRouting
  scan_test.go      unit tests + zero-alloc assertions for scanModel/resolveCluster
  cmd/main.go       wiring: Register + RegisterHttpFilterConfigFactories
  envoy.yaml        cluster_header route + openai/anthropic/default clusters (TLS)
```

## Key patterns

**StopAllAndBuffer then scan.** `OnRequestHeaders` returns
`HeadersStatusStopAllAndBuffer`, which tells Envoy to hold the request and buffer
the body before calling `OnRequestBody`. Only `OnRequestBody` with `endStream=true`
does real work. Non-final chunks return `BodyStatusStopAndBuffer` to keep buffering.

**gjson for model extraction.** `modelFromBody` uses `unsafe.String` to convert
the Envoy-owned `[]byte` to a string without copying, then calls `gjson.Get(s, "model").Str`.
`gjson.Get` on a string input is 0 allocs for unescaped values: it returns a `Result`
whose `.Str` field is a sub-slice of the input string. No heap.
`gjson.GetBytes` would cost 1 alloc (internal `string(data)` conversion); use `Get`.

**cluster_header routing.** Rather than modifying xDS or using per-request metadata,
the filter writes `x-cluster: <cluster>` and calls `ClearRouteCache`. Envoy's
`cluster_header` route reads the header and routes accordingly. Zero xDS traffic,
works with any static Envoy config.

**HeadTail ring, observe mode.** The response handler never returns
`BodyStatusStopAndWatermark`. It always returns `BodyStatusContinue`: Envoy streams
chunks to the downstream client while the filter writes them into the ring in parallel.
The ring captures at most 8 KB + 64 KB regardless of stream length. Token usage
appears in the first and last events of the stream, which is exactly what the ring
captures.

**Pool covers ring allocation.** Each `Filter` in the `sync.Pool` carries its own
`*buffer.HeadTail`. The ring is allocated once when `sync.Pool.New` runs, then reused
via `ring.Reset()` in `Factory.Create`. No per-request allocation for the ring.

## Allocation profile

Benchmark on the fake handle (eliminates ABI-level noise):

```
BenchmarkLLMProxy_ModelRouting-8   16M ops   72 ns/op   0 B/op   0 allocs/op
```

`modelFromBody` (`unsafe.String` + `gjson.Get`), `resolveCluster`, pool get/put,
and ring reset are all 0-alloc.

On real Envoy with `CGO_ENABLED=1`, two additional ABI-level allocations apply:

- `GetChunks()`: 2 allocs per `OnResponseBody` call (C array to Go slice conversion,
  structural to the ABI).
- `getSingleHeader` CGO escape: 1 alloc per `GetOneInto` call on some ABI versions.

See `bench/` for the full baseline with real CGO.

## Difference from sahl/examples/decoder

|                 | llm-proxy (this)         | sahl/examples/decoder        |
|-----------------|--------------------------|------------------------------|
| Layer           | raw luwes                | sahl ergonomic layer         |
| Alloc floor     | 0 (filter code)          | sahl overhead applies        |
| Body scan       | zero-alloc manual scan   | sahl request body API        |
| Code lines      | ~200                     | ~90                          |
| Response tap    | HeadTail SSE ring        | planned                      |
| Use when        | max performance, 0-alloc proof | readable production code |
