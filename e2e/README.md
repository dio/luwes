# e2e

Integration tests for luwes filters against a real Envoy binary.

## What it covers

| Filter | Port | Test cases |
|--------|------|------------|
| `header-auth` (raw luwes SDK) | 10000 | accept, reject, empty key, x-user-id injected |
| `header-auth-sahl` (sahl layer) | 10001 | same contract as above |
| `sse-tap` (sahl response observer) | 10002 | Anthropic SSE, OpenAI SSE, non-SSE skip, counter increments |

All three filters are compiled into a single `libe2e.so` from `e2e/cmd/main.go`.
Envoy loads it once; each filter gets its own listener port.

## Prerequisites

- Envoy binary at `.bin/envoy` (run `make .bin/envoy` from the project root)
- Go with CGO enabled

## Run

```sh
# From the project root:
make e2e

# Or manually:
ENVOY_BIN=.bin/envoy go test -C e2e -v -timeout=90s ./...

# Skip the .so build (reuse existing e2e/libe2e.so):
LUWES_SKIP_BUILD=1 ENVOY_BIN=.bin/envoy go test -C e2e -v -timeout=90s ./...
```

Tests skip automatically when `ENVOY_BIN` is not present.

## How it works

`TestMain` in `main_test.go`:
1. Builds `e2e/libe2e.so` from `e2e/cmd/main.go` via `go build -buildmode=c-shared`
2. Starts a mock SSE upstream on a dynamic port (for the sse-tap tests)
3. Writes a temporary Envoy config with three listeners and an `sse_upstream` cluster
4. Starts Envoy and waits up to 15 s for the admin `/ready` endpoint
5. Runs all test suites, then kills Envoy and the mock SSE server

The mock SSE server (`sse_tap_test.go`) serves:
- `POST /anthropic` — Anthropic-format SSE (message_start + message_delta)
- `POST /openai` — OpenAI chat format (usage chunk + [DONE])
- `POST /non-sse` — plain JSON (verifies filter skips non-SSE)

## File structure

```
e2e/
  main_test.go         TestMain: build .so, mock SSE, start Envoy, teardown
  header_auth_test.go  HeaderAuthSuite + HeaderAuthSahlSuite (8 tests each)
  sse_tap_test.go      SSETapSuite (6 tests) + startMockSSEServer
  cmd/main.go          combined .so: header-auth + header-auth-sahl + sse-tap
  go.mod               separate module (e2e deps don't pollute root)
  envoy.yaml           static Envoy config used by make e2e (single-filter dev)
```
