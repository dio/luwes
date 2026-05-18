# request-logger

Records the full observable state of every request: headers, body (optional),
upstream identity, error signals, latency, and trace context. Emits one
structured log record per request at stream completion. When an OTel tracing
provider is wired, all fields are also set as span tags so every log record
links to its trace in Jaeger or Tempo without a secondary lookup.

Designed as a base layer for a request/response recorder that feeds into a
backend store (NDJSON, Parquet, DuckDB) for offline analysis.

## What it records

Every request accumulates a [record] across four callbacks:

| Callback | Fields captured |
|----------|----------------|
| `OnRequestHeaders` | `request_id`, `method`, `path`, `host`, `trace_id`, `span_id`, request headers (configurable) |
| `OnRequestBody` | `request_body` (when `record_request_body=true`) |
| `OnResponseHeaders` | `upstream_status`, `upstream_address`, response headers (configurable) |
| `OnLocalReply` | `error_details` (Envoy-generated errors: timeout, circuit breaker, rate limit) |
| `OnStreamComplete` | deposits partial record into `pendingRecords`; span tagging |
| `on_access_logger_log` | `duration_ms`, `request_size_bytes`, `response_size_bytes`, `response_code`, `response_flags`, `response_code_details`, `upstream_failure` (finalized values) |

At stream completion, all fields are:

1. Written to dynamic metadata namespace `req_log` so access log formatters
   can read them via `%DYNAMIC_METADATA(req_log:field)%`.
2. Set as span tags on the active OTel span so fields appear as span attributes
   in Jaeger/Tempo/Grafana Tempo.
3. Emitted as a structured log line to Envoy's error log by the access logger
   after all finalized fields are populated.

Fields marked "finalized" (`duration_ms`, `response_flags`, `response_code_details`,
byte sizes) are unavailable from HTTP filter callbacks. They are populated by
the dynamic module access logger (`on_access_logger_log`), which fires after Envoy
finalizes `StreamInfo`, after `OnStreamComplete` returns.

## Config

Per-listener JSON config (set in `filter_config` in envoy.yaml):

```json
{
  "record_request_headers":  true,
  "record_response_headers": true,
  "record_request_body":     false,
  "record_response_body":    false,
  "max_body_bytes":          4096
}
```

Body recording buffers the full body before forwarding upstream. This adds
latency equal to the body transit time and increases memory pressure. Leave
disabled for low-latency paths; enable when you need payload-level debugging
(e.g. recording LLM prompts and completions for analysis or replay).

## Error signals

The filter detects errors from three independent sources:

**`error_details`** (from `OnLocalReply`): Envoy-generated local reply. Fires
when an upstream timeout expires, a circuit breaker opens, a rate limiter trips,
or a buffer overflows. The `details` string identifies the exact reason:
`upstream_reset_before_response_started`, `response_timeout`,
`upstream_overflow`, etc.

**`response_flags`** (from `GetResponseFlags()` uint64 bitmask via the access
logger, converted to comma-separated string): Envoy response flags. Key flags:

| Flag | Meaning |
|------|---------|
| `UF` | Upstream connection failure |
| `UH` | No healthy upstream hosts |
| `UC` | Upstream connection termination |
| `UT` | Upstream timeout |
| `UO` | Upstream overflow (circuit breaker) |
| `DC` | Downstream connection termination (client disconnected) |
| `RL` | Rate limited |
| `NR` | No route found |

**`upstream_failure`** (from `GetAttributeString(AttributeIDUpstreamTransportFailureReason)`):
TLS and transport-level failure reason. Non-empty when the upstream TLS handshake
or TCP connection failed with a specific cause.

## Trace correlation

At request ingress, the filter reads `trace_id` and `span_id` from the active
Envoy tracing span (`GetActiveSpan().GetTraceID()`). These are the same IDs the
OTel access log exporter stamps on every log record. The result: every row in your
log store has a `trace_id` you can paste directly into Jaeger or Tempo.

At stream completion, all recorded fields are set as span tags. This means the
trace itself carries the full error context so you can jump from a metric alert
directly to the trace without a separate log query.

## Prerequisites

- Go 1.22+ with CGO enabled
- Envoy downloaded automatically by `make run`
- Optional: an OTel collector for traces and structured access logs
  (otel-front, Grafana Agent, or any OTLP gRPC receiver on port 4317)

## Make targets

```sh
make build EXAMPLE=request-logger
make run   EXAMPLE=request-logger
make examples/test/examples/request-logger
```

## Manual steps

**1. Build**

```sh
CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
  -o dist/librequest-logger.so ./examples/request-logger/cmd
```

**2. Start the test backend**

A Go backend server is included. It serves `/ok`, `/slow`, `/error`,
`/notfound`, and LLM-style POST endpoints on `127.0.0.1:11000`:

```sh
go run ./examples/request-logger/testserver
```

**3. Start Envoy**

```sh
make run EXAMPLE=request-logger
# or manually:
GODEBUG=cgocheck=0 \
ENVOY_DYNAMIC_MODULES_SEARCH_PATH=$(pwd)/dist \
.bin/envoy -c examples/request-logger/envoy.yaml --log-level warning
```

**4. Run the test client**

```sh
go run ./examples/request-logger/testclient
```

Expected output:

```
METHOD PATH                           STATUS      DUR
-------------------------------------------------------
GET    /ok                               200      2ms
GET    /health                           200      1ms
GET    /error                            500      3ms
GET    /notfound                         404      1ms
GET    /slow                             200   1502ms
POST   /v1/chat/completions              200      4ms
POST   /v1/messages                      200      3ms
GET    /delayed (cancelled)                0    200ms
GET    /ok                               200      1ms
GET    /ok                               200      1ms
-------------------------------------------------------
done: check Envoy stdout for JSON access log records
```

The `/delayed` row is the downstream cancellation scenario: Envoy's fault filter
holds the request for 5 seconds before forwarding to upstream. The client cancels
after 200ms. Upstream is never contacted. In the Envoy access log you will see:

```json
{"method":"GET","path":"/delayed","status":0,"flags":"DC","duration_ms":200,
 "upstream_host":"-","code_details":"downstream_remote_disconnect"}
```

`DC` (downstream connection termination) and `status=0` confirm upstream was
never reached. This is the key distinction from `UT` (upstream timeout) where
Envoy did attempt the upstream but it timed out.

Each request produces a JSON access log record on Envoy's stdout:

```json
{
  "request_id": "a1b2c3d4-...",
  "method": "GET",
  "path": "/slow",
  "status": 200,
  "flags": "-",
  "duration_ms": 1502,
  "upstream": "127.0.0.1:11000",
  "details": "via_upstream",
  "trace_id": "",
  "error_details": ""
}
```

**5. Send individual requests**

```sh
# Normal
curl -si http://localhost:10000/ok

# Upstream 500 (exercises error logging)
curl -si http://localhost:10000/error

# Slow (exercises duration field)
curl -si http://localhost:10000/slow

# LLM-style POST
curl -si http://localhost:10000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}'

# Check Envoy admin stats:
curl -s http://127.0.0.1:9901/stats | grep request_logger
```

**6. Enable body recording**

Edit `envoy.yaml`, set `record_request_body: true` in filter_config, restart Envoy.
Then POST a request and check the `request_body` field in the access log.

## OTel wiring

**Collector (otel-front for local dev):**

```sh
# Install: go install github.com/vmihailenco/otel-front@latest
otel-front
# Receives OTLP gRPC on :4317, serves UI on http://localhost:8000
```

Uncomment the OTel tracing provider and OTel access log exporter blocks in
`envoy.yaml`, then restart Envoy. After a few requests:

- Traces appear in the otel-front UI at http://localhost:8000 with all span tags
  (`request.id`, `request.method`, `response.flags`, `error.details`, etc.)
- Access log records are OTel LogRecords with `trace_id` and `span_id` so you can
  click from a log entry directly into the trace.

## Analysis with DuckDB

The access log JSON format writes one record per line. Redirect Envoy's stdout
to a file and query it with DuckDB for offline analysis.

**Collect logs:**

```sh
GODEBUG=cgocheck=0 \
ENVOY_DYNAMIC_MODULES_SEARCH_PATH=$(pwd)/dist \
.bin/envoy -c examples/request-logger/envoy.yaml --log-level warning \
  > /tmp/access.ndjson 2>/dev/null &

# Run the test client to generate some traffic:
go run ./examples/request-logger/testclient
```

**Query with DuckDB:**

```sql
-- Install: pip install duckdb or brew install duckdb

-- All errors in the last hour
SELECT request_id, method, path, status, flags, error_details, duration_ms
FROM read_json_auto('/tmp/access.ndjson')
WHERE error_details != '' OR flags NOT IN ('', '-')
ORDER BY duration_ms DESC
LIMIT 50;

-- p50/p95/p99 latency per path
SELECT path,
       percentile_cont(0.5)  WITHIN GROUP (ORDER BY duration_ms) AS p50,
       percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms) AS p95,
       percentile_cont(0.99) WITHIN GROUP (ORDER BY duration_ms) AS p99,
       count(*) AS n
FROM read_json_auto('/tmp/access.ndjson')
GROUP BY path
ORDER BY p99 DESC;

-- Upstream failures grouped by reason
SELECT upstream_failure, count(*) AS n
FROM read_json_auto('/tmp/access.ndjson')
WHERE upstream_failure != ''
GROUP BY upstream_failure
ORDER BY n DESC;

-- All requests for a specific trace (paste trace_id from OTel UI)
SELECT * FROM read_json_auto('/tmp/access.ndjson')
WHERE trace_id = '4bf92f3577b34da6a3ce929d0e0e4736';
```

For production, write Parquet with hourly partitions via a collector pipeline
(OpenTelemetry Collector -> File Exporter -> Parquet sink) and query with:

```sql
SELECT * FROM read_parquet('/data/req_logs/2026/**/*.parquet')
WHERE error_details != '' AND duration_ms > 500;
```

## Filter structure

```
examples/request-logger/
  request_logger.go      Filter, Factory (config-driven), all callbacks
  request_logger_test.go 11 tests: config, happy path, errors, body, headers, pool
  cmd/main.go            wiring: Register + RegisterHttpFilterConfigFactories
  envoy.yaml             OTel tracing + JSON access log + OTel access log (commented)
  testserver/main.go     Go backend server (go run ./testserver)
  testclient/main.go     Go test client (go run ./testclient)
```

## Key patterns

**Config drives cost.** Body buffering, header copying, and log emission are all
gated on config. The hot path (headers only, no bodies, no OTel) is a few header
reads and metadata writes per request.

**Finalized attributes are not available in HTTP filter callbacks.**
`request.duration`, `response.flags`, `response.code_details`, byte sizes, and
`upstream.transport_failure_reason` all return nothing from `OnStreamComplete`.
Envoy finalizes `StreamInfo` only after the access log phase, which fires after
the filter chain completes. The access logger (`on_access_logger_log`) is the
correct place to read them.

**How the HTTP filter and access logger share state (the pending map).**

The two extension points run on the same Envoy worker thread but there is no
direct argument passing between them. The bridge is a `sync.Map` on the shared
`Factory`:

```
OnRequestHeaders  -> builds rec, stores request fields
OnResponseHeaders -> stores upstream_status, upstream_address
OnLocalReply      -> stores error_details (Envoy-generated timeout/circuit-breaker)
OnStreamComplete  -> copies rec into Factory.pendingRecords[request_id]
                     tags the active OTel span
                     does NOT emit the log line yet
                     |
                     v (Envoy finalizes StreamInfo)
                     |
on_access_logger_log -> pops Factory.pendingRecords[request_id]
                        fills in duration_ms, byte counts, response_flags,
                        response_code_details, upstream_failure
                        emits the final log line
                     |
OnDestroy         -> deletes Factory.pendingRecords[request_id] if still present
                     (safety cleanup when no access logger is wired)
```

The correlation key is `x-request-id`. Envoy generates and propagates this header
automatically. `OnRequestHeaders` captures it; the access logger reads it back
via `h.GetHeader(HttpHeaderTypeRequest, "x-request-id")` to find the matching
pending record.

Lifecycle safety: `OnDestroy` fires after `on_access_logger_log`. If the access
logger pops and deletes the entry in `LoadAndDelete`, `OnDestroy` finds nothing
and is a no-op. If no access logger is wired (or the logger fires for a different
request type), `OnDestroy` cleans up the entry so the map never leaks.

**`OnLocalReply` fires before `OnResponseHeaders`.** When Envoy generates a
local reply (upstream timeout, circuit breaker open, rate limit), it calls
`OnLocalReply` first with the details string, then synthesizes a response and
calls `OnResponseHeaders`. The `error_details` field is set in `OnLocalReply`
and carried in the record through `pendingRecords` to the access logger.

**`GetActiveSpan()` is nil when no tracing provider is configured.** All span
operations guard for nil. Removing the tracing provider from envoy.yaml makes
the filter skip all span tagging at zero cost (one nil check per callback).

**`copyHeaders` copies on the hot path.** `GetAll()` returns `UnsafeEnvoyBuffer`
values valid only during the callback. Every header name and value must be copied
into Go memory before returning. This is the correct and only safe approach.