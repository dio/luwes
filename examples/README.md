# examples

Examples for luwes and sahl. Each example has a `README.md` with build and
run instructions, an `envoy.yaml`, and unit tests (no Envoy required).

## Raw SDK examples

These examples use the luwes SDK directly: explicit pool management, full
lifecycle control, access to the complete attribute and span API surface.

| Example | What it shows |
|---------|--------------|
| [hello](hello/) | Minimal filter: read `:path`, stamp response header |
| [header-auth](header-auth/) | API key auth, `sync.Pool`, `GetOneInto` for 0-alloc header reads |
| [llm-proxy](llm-proxy/) | Model routing via `gjson`, HeadTail SSE ring, `cluster_header` |
| [error-handling](error-handling/) | Callout init/net/upstream errors, `OnLocalReply`, response flags |
| [request-logger](request-logger/) | Full request recorder: headers, body, OTel span tags, DuckDB analysis |
| [observability](observability/) | Metrics (counter + histogram), tracing (span tags + child spans), structured log enrichment |

## sahl examples

These examples use [sahl](../sahl/): the ergonomic layer built on top of luwes.
sahl trades 3 fixed allocations per request for a clean handler signature,
pooled per-request state, and built-in support for body buffering, response
observation, callouts, and per-listener factory isolation.

| Example | What it shows |
|---------|--------------|
| [sahl/header-auth](../sahl/examples/header-auth/) | Same auth filter written with sahl (`Register`) |
| [sahl/auth](../sahl/examples/auth/) | `RegisterFactory`: per-listener config isolation, two listeners from one .so |
| [sahl/decoder](../sahl/examples/decoder/) | Body-aware routing, SSE token tap, `r.Body()`, `w.ClearRouteCache()` |
| [sahl/sse-tap](../sahl/examples/sse-tap/) | Response observer: zero-latency SSE token counting, `HeadTail` ring |
| [sahl/spa](../sahl/examples/spa/) | Embedded Vite SPA (`//go:embed`) + JSON API handler, two filters in one .so |
| [sahl/request-ui](../sahl/examples/request-ui/) | E2e request recorder: Postgres or in-memory sink, SSE live UI, Docker Compose |

## Build any example

```sh
# Raw SDK examples
make build EXAMPLE=<name>          # e.g. header-auth, llm-proxy, error-handling
make run   EXAMPLE=<name>

# sahl examples
make build EXAMPLE=sahl/<name>     # e.g. sahl/decoder, sahl/sse-tap, sahl/request-ui
make run   EXAMPLE=sahl/<name>

# Run unit tests (no Envoy required)
make examples/test/examples/<name>
make examples/test/sahl/examples/<name>
```
