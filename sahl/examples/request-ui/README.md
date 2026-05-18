# request-ui

End-to-end request/response recorder: records every request (success and error)
and serves a near-realtime web UI for browsing, searching, and inspecting the
full detail of each request.

Two storage backends, selected by `REQUI_MODE`:

- `memory` (default for simulate): in-process ring buffer, zero dependencies
- `postgres`: persists to Postgres, survives restarts, full-text search

## Architecture

```
Envoy worker thread
  -> request-ui sahl filter (HTTP filter)
     -> response headers: collect method, path, host, upstream address, error_details
       -> deposits partial Record into pendingRecords (keyed by x-request-id)
  -> request-ui access logger (fires after stream finalization)
     -> reads finalized: duration, byte counts, wire bytes, response flags,
        code_details, upstream_failure, protocol, retry count, cx pool time,
        trace/span IDs, local reply body
     -> pops Record from pendingRecords, enriches, sends to sink channel
  -> sink goroutine (100ms batch INSERT via COPY)
     -> Postgres: requests table
     -> broadcaster: fan-out to SSE subscribers
  -> HTTP server (port 6062)
       GET /                        embedded web UI
       GET /api/requests            last 500 records, newest first
       GET /api/requests?since=ID   incremental poll (id > N, ascending)
       GET /api/requests?q=TEXT     iLIKE search across all error/path/method fields
       GET /api/requests?errors=1   only records where has_error = true
       GET /api/stream              SSE: new records pushed in real time
```

## What it records

Every request gets one row in the `requests` table:

| Field | Source | Notes |
|-------|--------|-------|
| `request_id` | `x-request-id` header | Envoy generates this; correlation key |
| `method` | request header `:method` | GET, POST, etc. |
| `path` | request header `:path` | full path including query |
| `host` | request header `:authority` | virtual host |
| `trace_id` | access logger `GetTraceID()` | empty if no tracing provider |
| `span_id` | access logger `GetSpanID()` | empty if no tracing provider |
| `trace_sampled` | access logger `IsTraceSampled()` | whether request was sampled |
| `request_headers` | `chunk.Headers.GetAll()` (response phase) | JSON array `[[k,v],...]` |
| `request_body` | `r.Body()` (request phase, optional) | truncated at `max_body_bytes` |
| `request_protocol` | `AttributeIDRequestProtocol` | HTTP/1.1, HTTP/2, HTTP/3 |
| `upstream_status` | HTTP status from response headers | e.g. "200", "503" |
| `upstream_address` | `AttributeIDUpstreamAddress` | IP:port of selected upstream host |
| `upstream_local_address` | `AttributeIDUpstreamLocalAddress` | our side of the upstream connection |
| `upstream_request_attempts` | `GetUpstreamRequestAttemptCount()` | >1 means retries occurred |
| `upstream_cx_pool_ready_ms` | `GetUpstreamPoolReadyDurationNs()` | wait time for upstream connection |
| `response_headers` | `chunk.Headers.GetAll()` (response headers call) | JSON array |
| `response_body` | response body buffer (optional) | truncated at `max_body_bytes` |
| `error_details` | `OnLocalReply` via `w.LocalReplyDetails()` | Envoy-generated errors only |
| `response_flags` | `GetResponseFlags()` bitmask converted to string | see table below |
| `response_code_details` | `AttributeIDResponseCodeDetails` | e.g. "via_upstream", "response_timeout" |
| `upstream_failure` | `AttributeIDUpstreamTransportFailureReason` | TLS/transport failures |
| `local_reply_body` | `GetLocalReplyBody()` | body of Envoy-synthesized responses |
| `duration_ms` | `GetTimingInfo().RequestCompleteDurationNs` | full stream duration |
| `request_size_bytes` | `GetBytesInfo().BytesReceived` | bytes received from downstream |
| `response_size_bytes` | `GetBytesInfo().BytesSent` | bytes sent to downstream |
| `wire_bytes_received` | `GetBytesInfo().WireBytesReceived` | includes TLS overhead |
| `wire_bytes_sent` | `GetBytesInfo().WireBytesSent` | includes TLS overhead |
| `response_code` | `AttributeIDResponseCode` | final HTTP status code |
| `has_error` | derived | true when any error signal is set |

## Envoy response flags

Reference: https://www.envoyproxy.io/docs/envoy/latest/configuration/observability/access_log/usage#config-access-log-format-response-flags

The `response_flags` field is set by Envoy after stream resolution. A request can
have multiple flags (comma-separated). The filter marks `has_error=true` when any
of the upstream failure flags are present.

| Flag | Category | Meaning |
|------|----------|---------|
| `UF` | upstream | Upstream connection failure |
| `UH` | upstream | No healthy upstream hosts in cluster |
| `UC` | upstream | Upstream connection termination |
| `UT` | upstream | Upstream request timeout |
| `UO` | upstream | Upstream overflow (circuit breaker triggered) |
| `URX` | upstream | Upstream retry limit exceeded |
| `UI` | upstream | Upstream remote reset (RST_STREAM) |
| `UR` | upstream | Upstream remote reset (connection-level) |
| `LR` | local | Local reset of connection |
| `LH` | local | Local health check failure |
| `RL` | throttle | Rate limited by Envoy |
| `RLSE` | throttle | Rate limited (service error) |
| `DC` | downstream | Downstream remote disconnected (client hung up) |
| `DT` | downstream | Downstream timeout |
| `NR` | routing | No route found (404 from routing) |
| `NC` | routing | No cluster found |
| `DPE` | protocol | Downstream protocol error |
| `UPE` | protocol | Upstream protocol error |
| `UMSDR` | protocol | Upstream maximum stream duration reached |
| `FI` | filter | Fault injection active |
| `RESP` | filter | Via response action (ext_proc or similar) |
| `-` | ok | No flags (clean request) |

`response_flags` is finalized by Envoy after stream completion. It is not
available in any HTTP filter callback. The access logger reads it via
`h.GetResponseFlags()` and converts the bitmask with `shared.ResponseFlagsString`.

> **ABI note:** `response.flags`, `response.code_details`, `request.duration`,
> and byte sizes are unavailable from HTTP filter callbacks. They are finalized
> by Envoy only after the access log phase. This filter reads them in
> `on_access_logger_log` via `PendingRecords`; see
> [How the pending map works](#how-the-pending-map-works) below.

## How the pending map works

The HTTP filter (sahl) and the access logger run as separate Envoy extension
points on the same worker thread. There is no direct argument passing between
them. The bridge is `PendingRecords`: a `sync.Map` on the shared `Factory`,
keyed by `x-request-id`.

```
response handler (sahl)
  chunk.StatusCode != 0  -> buildRecord: collect method, path, host,
                            upstream_address, error_details, response headers
                         -> pending.Store(requestID, record)

on_access_logger_log (access logger, fires after stream finalization)
                         -> h.GetHeader(Request, "x-request-id") = key
                         -> pending.LoadAndDelete(key) = record
                         -> enrich: duration_ms, byte counts, response_flags,
                            code_details, upstream_failure, protocol, ...
                         -> sink.Send(record)  // appears in UI

OnDestroy (HTTP filter)  -> pending.Delete(requestID)
                            // safety cleanup if access logger never ran
```

The correlation key is `x-request-id`, generated by Envoy automatically.

**Why `LoadAndDelete`:** the access logger takes ownership of the record on
the first call. `OnDestroy` finds nothing and is a no-op. This prevents
double-processing even if `on_access_logger_log` fires multiple times for
the same stream (it should not, but defensive programming).

**DC (client disconnect) fallback:** when the client disconnects before
upstream responds, the sahl response handler never fires, so no record is
deposited. The access logger detects the missing entry and calls
`buildMinimalRecord`, which reads `:method`, `:path`, `:authority` directly
from the access logger's request header map. The record will have
`response_flags: "DC"` and `response_code_details: "downstream_remote_disconnect"`
but no `upstream_status` or response headers (the response never arrived).

For the full lifecycle diagram and a detailed explanation of the ordering
guarantees, see the
[request-logger Key patterns](../../../examples/request-logger/README.md#key-patterns)
section, which uses the same mechanism.

## `error_details` vs `response_flags` vs `response_code_details`

Three independent error signals:

**`error_details`** (from `OnLocalReply`): Envoy generated the response itself --
timeout, circuit breaker open, rate limit, buffer overflow. The string is Envoy's
internal error code, e.g.:
- `upstream_reset_before_response_started{connection_failure}`
- `upstream_reset_after_response_started{connection_failure}`
- `response_timeout`
- `upstream_overflow`
- `local_rate_limit`
- `buffer_limit_exceeded`

Not set when the upstream returned a normal response (even a 500).

**`response_flags`** (from stream attributes): summary flags on the completed stream.
Coarser than `error_details` but always present. `UF` and `UC` usually correspond
to the reset variants of `error_details`.

**`response_code_details`** (from stream attributes): the definitive detail string
on the completed stream. `via_upstream` means the upstream sent the response.
Anything else is an Envoy-generated response, the same set as `error_details` but
available as an attribute rather than in `OnLocalReply`. Use this in `OnStreamComplete`
when you need to distinguish a real upstream 503 (`via_upstream`) from an Envoy
timeout 503 (`response_timeout`).

## Config

Per-listener JSON config in `envoy.yaml` `filter_config`:

```json
{
  "record_request_headers":  true,
  "record_response_headers": true,
  "record_request_body":     false,
  "record_response_body":    false,
  "max_body_bytes":          4096
}
```

Body recording buffers the full body before forwarding. Adds latency.
Enable only when you need prompt/completion-level debugging.

## Quick start (Docker Compose)

**1. Build the .so for the correct Linux architecture:**

Docker Desktop on Apple Silicon runs arm64 containers; on x86 it runs amd64.
The `request-ui-docker` target detects your host arch automatically:

```sh
# From the repo root:
make request-ui-docker
```

Or manually (replace `arm64` with `amd64` on x86):

```sh
make build-linux-arm64 EXAMPLE=sahl/request-ui
cp dist/librequest-ui.linux-arm64.so dist/librequest-ui.so
```

**2. Start the stack:**

```sh
cd sahl/examples/request-ui
docker compose up
```

Services started:
- Postgres on :5432
- Mock upstream on :8080 (serves `/ok`, `/slow`, `/error`, `/notfound`)
- Envoy on :10000 (proxied upstream), :9901 (admin), :6062 (request-ui)
- load-gen: fires continuous mixed traffic automatically

**3. Open the UI:**

```
http://localhost:6062/
```

The table populates in real time via SSE. Color coding:
- Red rows: `has_error = true` (any upstream failure, 5xx, or Envoy-generated error)
- Yellow rows: `duration_ms > 500` (slow requests)
- Click any row to expand the full detail panel

**4. Filter and search:**

```sh
# API: all error records
curl http://localhost:6062/api/requests?errors=1 | jq .

# API: search by path
curl 'http://localhost:6062/api/requests?q=/api/v1' | jq .

# API: incremental poll since ID 42
curl 'http://localhost:6062/api/requests?since=42' | jq .
```

**5. Send test requests:**

```sh
# Normal
curl http://localhost:10000/ok

# Upstream 500
curl http://localhost:10000/error

# Upstream timeout (upstream sleeps 1.5s; Envoy timeout is 30s, adjust in envoy.yaml to trigger UT)
curl http://localhost:10000/slow

# 404 from upstream
curl http://localhost:10000/notfound
```

**6. Query Postgres directly:**

```sh
docker compose exec postgres psql -U requi -d requi -c \
  "SELECT method, path, response_code, response_flags, error_details, duration_ms
   FROM requests WHERE has_error ORDER BY id DESC LIMIT 20;"
```

## Run without Docker

**1. Start Postgres locally:**

```sh
docker run -d --name requi-pg \
  -e POSTGRES_USER=requi -e POSTGRES_PASSWORD=requi -e POSTGRES_DB=requi \
  -p 5432:5432 postgres:16-alpine
```

**2. Build the .so for your host OS:**

```sh
make build EXAMPLE=sahl/request-ui
```

**3. Start the test backend:**

```sh
go run ./sahl/examples/request-ui/testserver
```

**4. Start Envoy (uses envoy-local.yaml pointing at 127.0.0.1:11000):**

```sh
REQUI_DSN=postgres://requi:requi@localhost:5432/requi?sslmode=disable \
REQUI_ADDR=0.0.0.0:6062 \
ENVOY_YAML=$(pwd)/sahl/examples/request-ui/envoy-local.yaml \
make run EXAMPLE=sahl/request-ui
```

**5. Open http://localhost:6062/**

**6. Run the test client:**

```sh
go run ./sahl/examples/request-ui/testclient
```

Expected output:

```
METHOD PATH                                STATUS      DUR
------------------------------------------------------------
GET    /ok                                    200      2ms
GET    /health                                200      1ms
GET    /error                                 500      3ms
GET    /notfound                              404      1ms
GET    /slow                                  200   1502ms
POST   /v1/chat/completions                   200      4ms
POST   /v1/messages                           200      3ms
GET    /delayed (cancelled)                     -    200ms
GET    /ok                                    200      1ms
GET    /ok                                    200      1ms
------------------------------------------------------------
done: open http://localhost:6062/ to see all requests in the UI
```

Each row appears in the UI as it completes. Red rows: `has_error=true`.
Yellow rows: `duration_ms > 500`. The `/delayed (cancelled)` row shows
`flags=DC` in the detail panel: upstream was never contacted.

## Simulate (zero dependencies)

No Envoy, no Docker, no Postgres. The simulate command generates synthetic
traffic directly into an in-memory ring buffer and serves the same UI.

```sh
# Build once
go build -o /tmp/requi-simulate ./sahl/examples/request-ui/cmd/simulate/

# Run (memory mode is the default)
REQUI_ADDR=0.0.0.0:6062 /tmp/requi-simulate
```

Open http://localhost:6062/ and the table starts populating immediately at
~10 req/s. The generator covers every row type:

| Scenario | Rate | UI color |
|----------|------|----------|
| Normal 200 | 50% | white |
| Slow (>500ms) | 12% | yellow |
| Upstream 5xx | 8% | red |
| Upstream reset (UF) | 5% | red |
| Timeout (UT) | 6% | red |
| Circuit breaker (UO) | 4% | red |
| No route (NR) | 3% | red |
| Client disconnect (DC) | 4% | white (not an upstream error) |

The ring holds 2000 records by default. Override with `REQUI_MEM_CAP=N`.
History is lost on process exit.

To persist to Postgres instead:

```sh
REQUI_MODE=postgres \
REQUI_DSN=postgres://requi:requi@localhost:5432/requi?sslmode=disable \
REQUI_ADDR=0.0.0.0:6062 \
/tmp/requi-simulate
```

## Filter structure

```
sahl/examples/request-ui/
  filter.go              sahl filter: collects request/response state, emits to sink
  filter_test.go         unit tests: error detection helpers, config, pool
  sink/
    sink.go              Postgres + in-memory store, SSE broadcaster, HTTP API
    ui.go                embed directive for index.html
    index.html           single-page UI: SSE live table + search + detail panel
  cmd/main.go            wiring: starts sink, registers filter
  cmd/simulate/main.go   zero-dep traffic generator (REQUI_MODE=memory default)
  testserver/main.go     Go backend (go run ./testserver, port 11000)
  testclient/main.go     Go test client (go run ./testclient, port 10000)
  envoy.yaml             Docker Compose config (upstream:8080)
  envoy-local.yaml       Local dev config (127.0.0.1:11000, fault filter for /delayed)
  Dockerfile             Envoy runtime image (bind-mounts the .so)
  docker-compose.yml     full stack: Postgres + upstream + Envoy + load-gen
```

## sahl API additions in this example

### Writer

```go
// GetAttributeString, GetAttributeNumber, GetAttributeBool:
// delegate to the underlying handle; valid in any callback.
func (w *Writer) GetAttributeString(id shared.AttributeID) (shared.UnsafeEnvoyBuffer, bool)
func (w *Writer) GetAttributeNumber(id shared.AttributeID) (float64, bool)
func (w *Writer) GetAttributeBool(id shared.AttributeID) (bool, bool)

// ActiveSpan returns the Envoy tracing span for the current stream.
func (w *Writer) ActiveSpan() shared.Span

// LocalReplyDetails returns the details string from the most recent
// OnLocalReply callback (e.g. "response_timeout", "circuit_breaker_overflow").
// Non-empty only when Envoy generated the response; empty for upstream responses.
func (w *Writer) LocalReplyDetails() string
```

### AccessLoggerHandle (new in luwes)

The access logger receives a fully finalized `AccessLoggerHandle` after stream teardown:

```go
// Timing: all durations in nanoseconds, -1 = unavailable.
h.GetTimingInfo() TimingInfo
// Bytes: downstream bytes received/sent, wire bytes (including TLS overhead).
h.GetBytesInfo() BytesInfo
// Flags: uint64 bitmask of CoreResponseFlag enum, converted to "UF,UT" style string.
h.GetResponseFlags() uint64
// Response code, code details, transport failure reason.
h.GetResponseCode() uint32
h.GetAttributeString(AttributeIDResponseCodeDetails)
h.GetAttributeString(AttributeIDUpstreamTransportFailureReason)
// Upstream connection pool wait, retry count, local address.
h.GetUpstreamPoolReadyDurationNs() int64
h.GetUpstreamRequestAttemptCount() uint32
h.GetAttributeString(AttributeIDUpstreamLocalAddress)
// Tracing.
h.GetTraceID() (UnsafeEnvoyBuffer, bool)
h.GetSpanID() (UnsafeEnvoyBuffer, bool)
h.IsTraceSampled() bool
// Local reply body (Envoy-synthesized response body).
h.GetLocalReplyBody() (UnsafeEnvoyBuffer, bool)
// Request/response headers (all finalized).
h.GetHeader(headerType HttpHeaderType, key string) (UnsafeEnvoyBuffer, bool)
```