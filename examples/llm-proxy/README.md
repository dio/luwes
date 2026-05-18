# llm-proxy

Zero-allocation LLM proxy filter built on raw luwes (no sahl layer).

Routes requests to OpenAI, Anthropic, or a default cluster based on the `model`
field in the JSON request body. Taps SSE response streams for token usage
without buffering the full body.

## What it does

**Request phase:**

1. Returns `HeadersStatusStopAllAndBuffer` to buffer the request body.
2. Scans the body for `"model": "<value>"` via a zero-alloc byte scanner.
3. Maps model to cluster: `gpt-*` -> `openai`, `claude-*` -> `anthropic`, else `default`.
4. Sets `x-cluster` header and calls `ClearRouteCache` so Envoy's `cluster_header`
   route selects the right upstream.
5. Increments `llm_proxy_requests_total{cluster}`.

**Response phase:**

1. Checks `Content-Type: text/event-stream`. If absent, skips SSE tap.
2. Writes SSE chunks into a HeadTail ring buffer (8 KB head + 64 KB tail).
   The ring captures the first and last portions of the stream; the middle is
   never stored. Zero added latency: `BodyStatusContinue` on every chunk.
3. On `endStream=true`, scans head and tail for token usage in both OpenAI and
   Anthropic SSE formats.
4. Emits `llm_proxy_input_tokens{cluster}` and `llm_proxy_output_tokens{cluster}`.

## Allocation profile

Benchmark on the fake (eliminates ABI-level noise):

```
BenchmarkLLMProxy_ModelRouting-8   17M ops   70 ns/op   0 B/op   0 allocs/op
```

On real Envoy with `CGO_ENABLED=1`, two ABI-level allocations are unavoidable:

- `GetChunks()`: 2 allocs per `OnResponseBody` call (C array + Go slice conversion).
  These are structural to the ABI, not the filter code.
- `getSingleHeader` CGO escape: 1 alloc per `GetOneInto` call on some ABI versions.

The filter code itself: `scanModel`, `resolveCluster`, pool get/put -- is 0 allocs.
See the bench/ directory for the full baseline with real CGO.

## Run

Build the dynamic module:

```sh
make build
```

This produces `libllm-proxy.so` (or `.dylib` on macOS).

Start Envoy:

```sh
ENVOY_DYNAMIC_MODULES_SEARCH_PATH=$(pwd) \
  GODEBUG=cgocheck=0 \
  envoy -c envoy.yaml --log-level warning
```

Send a request:

```sh
curl -s http://localhost:10000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}'
```

Check metrics:

```sh
curl -s http://localhost:9901/stats | grep llm_proxy
```

## Cluster routing table

Edit `scan.go` to add or reorder routes. No map lookup, static prefix scan:

```go
var clusterRoutes = [...]routeEntry{
    {[]byte("gpt-"),    "openai"},
    {[]byte("o1"),      "openai"},
    {[]byte("o3"),      "openai"},
    {[]byte("claude-"), "anthropic"},
}
```

First match wins.

## Difference from sahl/examples/decoder

| | llm-proxy (this) | sahl/examples/decoder |
|---|---|---|
| Layer | raw luwes | sahl ergonomic layer |
| Alloc floor | 0 (filter code) | 3 (sahl overhead) |
| Body scan | zero-alloc manual scan | `json.Unmarshal` |
| Code lines | ~150 | ~90 |
| Response flags | no | planned |
| Use when | max performance, 0-alloc proof | readable production code |