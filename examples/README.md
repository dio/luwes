# examples

Examples for luwes and sahl.

## Raw SDK examples

These examples use the luwes SDK directly: zero allocations, explicit pool
management, full lifecycle control.

| Example | What it shows |
|---|---|
| [hello](hello/) | Minimal filter: read `:path`, stamp response header |
| [header-auth](header-auth/) | API key auth, `sync.Pool`, 0 allocs/op on hot path |
| [observability](observability/) | Metrics, tracing, structured logging |

## sahl examples

These examples use [sahl](../sahl/): the ergonomic layer built on top of luwes.
See [sahl/examples](../sahl/examples/) for the full list.

| Example | What it shows |
|---|---|
| [sahl/examples/header-auth](../sahl/examples/header-auth/) | Same auth filter written with sahl |
| [sahl/examples/auth](../sahl/examples/auth/) | `RegisterFactory`: per-listener config isolation |
| [sahl/examples/decoder](../sahl/examples/decoder/) | Body-aware routing, SSE token tap |
| [sahl/examples/sse-tap](../sahl/examples/sse-tap/) | Response observer: zero-latency SSE token counting |
| [sahl/examples/spa](../sahl/examples/spa/) | Embedded SPA + JSON API, two filters in one .so |
