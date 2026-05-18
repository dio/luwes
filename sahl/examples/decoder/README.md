# decoder

LLM model-based routing filter using sahl's body-aware handler API. Demonstrates
[sahl.RegisterWithBodyConfigAndResponse]: reads the JSON request body, maps the
model name to a provider cluster, sets routing headers, and taps token usage from
SSE and JSON responses.

## What it does

1. Reads the `model` field from the JSON request body (first 8 KB buffered)
2. Maps model name to a provider cluster: `openai`, `anthropic`, or `default`
3. Sets `x-cluster` header and calls `ClearRouteCache()` so Envoy's `cluster_header`
   route selects the right upstream
4. Sets filter metadata (`decoder:model`, `decoder:cluster`) for access logs
5. Emits per-cluster request counters
6. On the response side: taps token usage from SSE streams (Anthropic + OpenAI)
   and non-streaming JSON responses, emits per-cluster counters

## Model routing rules

| Prefix | Cluster |
|--------|---------|
| `gpt-*`, `o1*`, `o3*` | `openai` |
| `claude-*` | `anthropic` |
| unknown / empty | `default` |

## Metrics

| Metric | Type | Tags | Description |
|--------|------|------|-------------|
| `decoder_requests_total` | counter | `cluster` | Requests routed per provider |
| `decoder_input_tokens` | counter | `cluster` | Input tokens per provider |
| `decoder_output_tokens` | counter | `cluster` | Output tokens per provider |
| `decoder_ttft_ms` | histogram | `cluster` | Time to first token in ms |

```sh
curl -s http://localhost:9901/stats | grep decoder
```

## Envoy config wiring

Requires a `cluster_header` route and three clusters (`openai`, `anthropic`, `default`).
See `envoy.yaml` for a complete example pointing at real providers.

## Make targets

From this directory:

```sh
make build   # compile libdecoder.so
make run     # build + start Envoy (foreground, Ctrl-C to stop)
make test    # unit tests, no Envoy required
make clean   # remove built .so
```

From the repo root:

```sh
make build EXAMPLE=sahl/decoder
make run   EXAMPLE=sahl/decoder
```

Or manually (from repo root):

```sh
CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
  -o dist/libdecoder.so ./sahl/examples/decoder/cmd

GODEBUG=cgocheck=0 \
ENVOY_DYNAMIC_MODULES_SEARCH_PATH=$(pwd)/dist \
.bin/envoy -c sahl/examples/decoder/envoy.yaml --log-level warning
```

With Envoy running, in a separate terminal:

```sh
# Route to OpenAI
curl http://localhost:10000/v1/chat/completions \
  -H "authorization: Bearer $OPENAI_API_KEY" \
  -H "content-type: application/json" \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}'

# Route to Anthropic
curl http://localhost:10000/v1/messages \
  -H "x-api-key: $ANTHROPIC_API_KEY" \
  -H "content-type: application/json" \
  -d '{"model":"claude-haiku-3-5","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}'
```

## Allocation analysis

All numbers are for the real CGO path (live Envoy).

**Request side (body-aware handler):**

| Operation | allocs |
|-----------|--------|
| OnRequestHeaders (StopAllAndBuffer, no handler) | 3 (sahl pre-copies) |
| OnRequestBody: r.Body() buffered read | 1 (body []byte copy) |
| json.Unmarshal into stack-allocated struct | 0 (no heap escape) |
| SetRequestHeader, SetMetadata, IncrementCounter | 0 (queued in writer) |
| flush() mutations | 0 |
| **Request total** | **4** |

**Response side (response observer, SSE path):**

| Operation | allocs |
|-----------|--------|
| respState allocation on headers call | 1 |
| buffer.NewHeadTail (head slab + Ring) | 2 |
| ContentType ToString | 1 |
| Per-chunk Write into ring | 0 |
| extractSSEUsage scan | 0 |
| emitUsage (IncrementCounter, SetMetadata) | 0 (queued) |
| flushResponseMutations | 0 |
| **Response total (SSE)** | **~4** |

**Response side (JSON path):** 1 (respState, no ring) + 1 (jsonBuf append) + json.Unmarshal + 1 (emitUsage).

## What this demonstrates

- `sahl.RegisterWithBodyConfigAndResponse`: body-aware request handler + config + response observer
- `r.Body()`: returns the full buffered request body as `[]byte` (Go-owned)
- `w.ClearRouteCache()`: re-evaluate `cluster_header` route after setting `x-cluster`
- `w.SetMetadata()`: publish routing decisions for access logs and downstream filters
- `buffer.HeadTail` for SSE token extraction (shared with `sahl/examples/sse-tap`)
- JSON body parsing in the response observer for non-streaming responses

## Filter structure

```
sahl/examples/decoder/
  decoder.go         filter: routing, SSE tap, JSON tap
  decoder_test.go    unit tests: resolveCluster, extractSSEUsage, filter wiring
  cmd/main.go        wiring: register, abi_impl, sahl.Factories()
  envoy.yaml         Envoy config with cluster_header route + provider clusters
  README.md          this file
```
