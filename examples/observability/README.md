# observability

The reference example for all three observability signals in a luwes filter:
**metrics**, **tracing**, and **structured log enrichment**.

This is not a production filter. Every signal is shown clearly with comments
explaining each decision, including the allocation impact.

## What it demonstrates

### Metrics

Two metrics defined at config load time (once, not per-request):

| Metric | Type | Description |
|---|---|---|
| `luwes_observability_requests_total` | counter | Requests handled, no tags |
| `luwes_observability_handler_duration_ms` | histogram | Time from request headers to response headers |

Both use no tag keys, which means:
- `IncrementCounterValue(id, 1)` -- pure atomic add, no symbol table write
- `RecordHistogramValue(id, ms)` -- no tag args, no slice alloc

See the [stats analysis](../../docs/envoy-stats.md) for why tag cardinality matters.

After a few requests, check Envoy's stats endpoint:

```sh
curl http://127.0.0.1:9901/stats?filter=luwes
```

Expected output:

```
dynamicmodulescustom.luwes_observability_requests_total: 3
dynamicmodulescustom.luwes_observability_handler_duration_ms: P0(nan,1.025) P50(nan,1.05) P99(nan,1.075) ...
```

The `dynamicmodulescustom.` prefix is the default module stats namespace
(overridable via `metrics_namespace` in the filter config).

### Tracing

`GetActiveSpan()` returns the Envoy tracing span for the request (nil if no
tracing provider is configured). The filter:

1. Tags the active span with `luwes.filter=observability` and `http.method`
2. Spawns a child span `luwes.observability.request` for the filter's own work
3. Finishes the child immediately (filter is synchronous)

To see spans in a trace UI, configure a tracing provider in `envoy.yaml`:

```yaml
tracing:
  provider:
    name: envoy.tracers.zipkin
    typed_config:
      "@type": type.googleapis.com/envoy.config.trace.v3.ZipkinConfig
      collector_cluster: zipkin
      collector_endpoint: "/api/v2/spans"
      collector_endpoint_version: HTTP_JSON
```

Without a tracing provider, `GetActiveSpan()` returns nil and the tracing block
is skipped at zero cost.

### Log enrichment

`SetMetadata("luwes", "key", "value")` writes into Envoy's dynamic filter metadata
for this request. Envoy access log formatters can read these values via
`%DYNAMIC_METADATA(namespace:key)%`.

The `envoy.yaml` in this example configures a stdout access log with the format:

```
luwes.method=%DYNAMIC_METADATA(luwes:method)%
luwes.path=%DYNAMIC_METADATA(luwes:path)%
luwes.status=%DYNAMIC_METADATA(luwes:status)%
```

This means every access log line carries the method, path, and response status
as structured fields -- without extra log calls per request.

For Envoy's gRPC access log or OTel access log exporter, these metadata fields
appear as attributes on the log record.

The filter also calls `handle.Log(LogLevelDebug, ...)` for filter-level events,
guarded by `handle.LogEnabled(LogLevelDebug)` to avoid boxing arguments when
debug logging is disabled.

## Prerequisites

- Go 1.22+ with CGO enabled
- Envoy is downloaded automatically to `.bin/envoy` by `make run`
- For traces: a Zipkin/Jaeger/OTel collector (optional)

## Make targets

```sh
# Build the .so
make build EXAMPLE=observability

# Start Envoy with the filter
make run EXAMPLE=observability
```

## Manual steps

**1. Build**

```sh
CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
  -o dist/libobservability.so ./examples/observability/cmd
```

**2. Run Envoy**

```sh
GODEBUG=cgocheck=0 \
ENVOY_DYNAMIC_MODULES_SEARCH_PATH=$(pwd)/dist \
.bin/envoy -c examples/observability/envoy.yaml --log-level warning
```

**3. Send requests**

```sh
curl http://localhost:10000/v1/test
curl http://localhost:10000/api/users
curl -X POST http://localhost:10000/v1/chat/completions
```

**4. Check metrics**

```sh
curl http://127.0.0.1:9901/stats?filter=luwes
```

Expected:

```
dynamicmodulescustom.luwes_observability_requests_total: 3
dynamicmodulescustom.luwes_observability_handler_duration_ms: P0(...) P50(...) P99(...)
```

**5. Observe access log enrichment**

The Envoy process stdout shows access log lines with the metadata fields:

```
[2026-05-17T...] "GET /api/users HTTP/1.1"
status=200
duration=1ms
luwes.method=GET
luwes.path=/api/users
luwes.status=200
```

## Filter structure

```
examples/observability/
  observability.go   -- Factory (metrics), Filter (all three signals)
  cmd/main.go        -- Wiring: Register + RegisterHttpFilterConfigFactories
  envoy.yaml         -- Listener + access log format with metadata fields
```

## Key patterns

**Define metrics at config time, not per-request.** `DefineCounter` and
`DefineHistogram` allocate symbol table entries. Calling them in `OnRequestHeaders`
would allocate on every request. Call them once in `NewFactory`.

**No-tag metrics for the hot path.** `IncrementCounterValue(id, 1)` with no
variadic args is a single atomic add. No slice allocation, no symbol table write.
Tag-keyed metrics (`IncrementCounterValue(id, 1, "GET")`) add one slice alloc and
one symbol table write per request -- acceptable only when the tag value is
bounded and the dimension is worth it.

**SetMetadata over Log for structured access log fields.** `handle.Log` emits a
line to Envoy's error log. `SetMetadata` writes into the request's filter metadata,
which appears in access logs, OTel exports, and downstream filter metadata reads.
Use `SetMetadata` when you want the field in the access log for every request.
Use `handle.Log` when you want it in the error log for specific events.

**LogEnabled guard.** `handle.Log(Debug, "%s %s", method, path)` boxes `method`
and `path` into `interface{}` at the call site regardless of whether debug logging
is enabled. The `LogEnabled` check avoids this boxing when debug is off.

**GetActiveSpan is nil-safe.** All `dymSpan` methods check for nil. If no tracing
provider is configured, `GetActiveSpan()` returns nil and the tracing block costs
one CGO call (the nil check). On the negative path this is ~10ns, acceptable.
```
