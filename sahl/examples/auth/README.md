# auth

Demonstrates [sahl.RegisterFactory]: the pattern for filters that carry
per-listener state: a parsed config and isolated metric IDs.

## Why RegisterFactory

`sahl.Register` gives you a single global `HandlerFunc`. That is fine when all
requests share the same behavior. When you need each Envoy listener (or
virtual-host) to carry its own config (different allowed keys, different
upstream clusters, different metric scopes), use `RegisterFactory`.

Envoy calls the factory once per filter-chain with the per-listener JSON config
attached to the listener. The factory parses that JSON and returns a
`HandlerFunc` that closes over the parsed result. Each listener's handler is
independent; there is no shared mutable state between them.

```
factory registered once at startup
       |
       +-- listener :10000  -->  HandlerFunc_A  (allowed: key-admin, key-ops)
       +-- listener :10001  -->  HandlerFunc_B  (allowed: key-user, key-guest)
```

## Filter behavior

The filter reads `x-api-key` (configurable) from request headers and checks it
against the `allowed_keys` list for the listener it arrived on.

- Missing header: 401, `{"error":"missing x-api-key"}`
- Unknown key: 401, `{"error":"invalid x-api-key"}`
- Allowed key: request passes through with `x-authenticated-key` injected

A logging middleware wraps the handler and emits a structured log line with the
method, path, and metadata namespace so you can trace which listener handled
the request.

## Config

```json
{
  "allowed_keys": ["key-admin", "key-ops"],
  "header": "x-api-key",
  "metadata_ns": "auth.admin"
}
```

| Field         | Default      | Description                              |
|---------------|--------------|------------------------------------------|
| allowed_keys  | []           | Keys that are allowed through            |
| header        | x-api-key    | Header name to read the key from         |
| metadata_ns   | auth         | Envoy metadata namespace for logging     |

## Make targets

From this directory:

```sh
make build   # compile libauth.so
make run     # build + start Envoy with two-listener config (foreground)
make test    # unit tests, no Envoy required
make clean   # remove built .so
```

From the repo root:

```sh
make build EXAMPLE=sahl/auth
make run   EXAMPLE=sahl/auth
```

Or manually (from repo root):

```sh
CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
  -o dist/libauth.so ./sahl/examples/auth/cmd

GODEBUG=cgocheck=0 \
ENVOY_DYNAMIC_MODULES_SEARCH_PATH=$(pwd)/dist \
.bin/envoy -c sahl/examples/auth/envoy.yaml --log-level warning
```

Test per-listener isolation (in a separate terminal):

```sh
# Admin listener: key-admin allowed, key-user rejected
curl -si -H 'x-api-key: key-admin' localhost:10000/    # 200
curl -si -H 'x-api-key: key-user'  localhost:10000/    # 401 invalid key

# User listener: key-user allowed, key-admin rejected
curl -si -H 'x-api-key: key-user'  localhost:10001/    # 200
curl -si -H 'x-api-key: key-admin' localhost:10001/    # 401 invalid key

# Both listeners reject missing key
curl -si localhost:10000/                               # 401 missing key
curl -si localhost:10001/                               # 401 missing key
```

## Unit tests

```sh
make test          # from this directory
# or from repo root:
make examples/test/sahl/examples/auth
```

## Code structure

```
auth.go          Config, AuthFilter, factory registration
auth_test.go     6 unit tests (no Envoy binary needed)
cmd/main.go      module entry point (c-shared build target)
envoy.yaml       two listeners with different allowed_keys
```

## Allocation analysis

| Path              | Allocs per request | Notes                                  |
|-------------------|--------------------|----------------------------------------|
| allowed key       | 0                  | map lookup on unsafe string, no alloc  |
| rejected key      | 1                  | JSON error body via Send()             |
| missing key       | 1                  | JSON error body via Send()             |
| CGO (real Envoy)  | 2                  | header Peek + Envoy ABI boundary       |

The happy path (allowed key) allocates nothing after the factory is created.
The `allowed` map is built once at factory-creation time from the parsed config.
