# sse-tap

Tap an SSE (Server-Sent Events) response stream to extract token usage without
buffering the entire body. Demonstrates the sahl response observer API
([sahl.RegisterWithConfigAndResponse]).

## What it does

The filter sits on the response path and processes streaming LLM responses:

- Input tokens appear near the **start** of the stream (Anthropic `message_start`,
  OpenAI first usage chunk).
- Output tokens appear near the **end** (Anthropic `message_delta`, OpenAI final
  usage chunk).

It uses `buffer.HeadTail` from `github.com/dio/luwes/buffer` to capture the first 8 KB and last 64 KB of each
response. The middle of a large response is never stored. On stream completion,
it scans both regions to extract token counts and emits Envoy counters.

The response observer runs on the Envoy worker thread. Chunks are forwarded to
the downstream client as they arrive (BodyStatusContinue): zero added latency.

## SSE formats supported

| Provider | Input tokens | Output tokens |
|----------|-------------|---------------|
| Anthropic | `event: message_start` data | `event: message_delta` data |
| OpenAI chat | `data.usage.prompt_tokens` | `data.usage.completion_tokens` |
| OpenAI Responses API | `data.usage.input_tokens` | `data.usage.output_tokens` |

## Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `sse_tap_input_tokens` | counter | Cumulative input tokens observed |
| `sse_tap_output_tokens` | counter | Cumulative output tokens observed |

After a few requests, check Envoy's stats endpoint:

```sh
curl -s http://127.0.0.1:9901/stats | grep sse_tap
```

The `sse_tap` dynamic metadata is also available in access logs (see `envoy.yaml`):

```
input_tokens=%DYNAMIC_METADATA(sse_tap:input_tokens)%
output_tokens=%DYNAMIC_METADATA(sse_tap:output_tokens)%
```

## Make targets

From this directory:

```sh
make build   # compile libsse-tap.so
make run     # build + start Envoy (foreground, Ctrl-C to stop)
make test    # unit tests, no Envoy required
make clean   # remove built .so
```

From the repo root:

```sh
make build EXAMPLE=sahl/sse-tap
make run   EXAMPLE=sahl/sse-tap
```

Or manually (from repo root):

```sh
CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
  -o dist/libsse-tap.so ./sahl/examples/sse-tap/cmd

GODEBUG=cgocheck=0 \
ENVOY_DYNAMIC_MODULES_SEARCH_PATH=$(pwd)/dist \
.bin/envoy -c sahl/examples/sse-tap/envoy.yaml --log-level warning
```

Point `llm_backend` in `envoy.yaml` at a real LLM backend or a mock SSE server.
For quick local testing, a minimal mock that emits an Anthropic-format SSE stream:

```sh
python3 -c "
import http.server, time

class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        self.send_response(200)
        self.send_header('Content-Type', 'text/event-stream')
        self.end_headers()
        events = [
            'event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":42}}}\n\n',
            'event: content_block_delta\ndata: {\"delta\":{\"text\":\"Hello\"}}\n\n',
            'event: message_delta\ndata: {\"usage\":{\"output_tokens\":3}}\n\n',
            'event: message_stop\ndata: {}\n\n',
        ]
        for e in events:
            self.wfile.write(e.encode())
            self.wfile.flush()
            time.sleep(0.01)
    def log_message(self, *a): pass

http.server.HTTPServer(('127.0.0.1', 11000), H).serve_forever()
" &
```

Then send a request:

```sh
curl -si -X POST http://localhost:10000/v1/messages \
  -H "content-type: application/json" \
  -d '{"model":"claude-3","messages":[{"role":"user","content":"hi"}]}'
```

Check metrics:

```sh
curl -s http://127.0.0.1:9901/stats | grep sse_tap
# sse_tap_input_tokens: 42
# sse_tap_output_tokens: 3
```

## Allocation analysis

All numbers below are for the real CGO path (live Envoy).

**Per-request cost (request side):**

| Operation | allocs |
|-----------|--------|
| Filter pool get + reset (3 pre-copies) | 3 |
| SetRequestHeader("x-sse-tap", "1") | 0 |
| **Request total** | **3** |

**Per-response cost (response side, SSE path):**

| Operation | allocs |
|-----------|--------|
| responseState.ctx allocation (ringState struct) | 1 |
| buffer.NewHeadTail (head slab + Ring.data) | 2 |
| ContentType ToString (pre-copy on headers call) | 1 |
| Per-chunk Write into ring: zero-copy, no alloc | 0 |
| ExtractUsage scan (pure computation) | 0 |
| IncrementCounter (2x, no tag args) | 0 |
| SetMetadata (2x) | 2 (key+value strings) |
| **Response total (SSE)** | **~6** |

**Per-response cost (non-SSE path):** 1 alloc (ringState.skip=true, no ring allocated).

**ExtractUsage in isolation (BenchmarkExtractUsage):** 0 allocs. The scan is
pure byte manipulation: `bytes.IndexByte`, `json.Unmarshal` into stack-allocated
structs. No heap pressure on the scan path.

**Fake-path benchmark discrepancy:** the `BenchmarkExtractUsage` in
`sse_tap_test.go` runs fully in pure Go and shows 0 allocs. Response-observer
benchmarks would show higher counts due to interface dispatch (same fake-path
artifact as sahl request benchmarks).

## What this demonstrates

- `sahl.RegisterWithConfigAndResponse`: request + response handler wired together
- `sahl.ResponseHandlerFunc` and `*sahl.ResponseChunk`: the response observer API
- `chunk.Context` for per-request mutable state across header and body calls
- `buffer.HeadTail` for head+tail ring buffering of large streams (zero alloc per chunk)
- `w.IncrementCounter` + `w.SetMetadata` from a response observer
- `BodyStatusContinue` (observe mode): chunks forwarded to client, zero latency added
- `ExtractUsage` exported for unit testing without Envoy

## Unit tests

```sh
make test          # from this directory
# or from repo root:
make examples/test/sahl/examples/sse-tap
```

## Filter structure

```
sahl/examples/sse-tap/
  sse_tap.go         filter + ExtractUsage (pure Go, testable without Envoy)
  sse_tap_test.go    unit tests + BenchmarkExtractUsage
  cmd/main.go        wiring: register, abi_impl, sahl.Factories()
  envoy.yaml         Envoy config with cluster + access log metadata
  README.md          this file

github.com/dio/luwes/buffer   Ring + HeadTail (shared with other luwes filters)
```
