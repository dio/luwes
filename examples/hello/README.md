# hello

The simplest possible luwes filter. Reads the request `:path` header and stamps
an `x-hello` response header with the value `from-luwes path=<path>`. No config,
no metrics, no body reading.

This is the starting point for writing a new luwes filter.

## What it does

- `OnRequestHeaders`: reads `:path`, copies it into the filter struct
- `OnResponseHeaders`: sets `x-hello: from-luwes path=<path>` on the response

## Prerequisites

- Go 1.22+ with CGO enabled
- No other dependencies required. Envoy is downloaded automatically by `make run`.

## Make targets

```sh
# Build the .so
make build EXAMPLE=hello

# Start Envoy with the filter (runs in foreground, Ctrl-C to stop)
make run EXAMPLE=hello
```

## Manual steps

**1. Build**

```sh
CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
  -o dist/libhello.so ./examples/hello/cmd
```

**2. Run Envoy**

Envoy is downloaded automatically to `.bin/envoy` on first run:

```sh
make run EXAMPLE=hello
```

Or manually:

```sh
GODEBUG=cgocheck=0 \
ENVOY_DYNAMIC_MODULES_SEARCH_PATH=$(pwd)/dist \
.bin/envoy -c examples/hello/envoy.yaml --log-level warning
```

**3. Test**

In a separate terminal:

```sh
# Check Envoy is ready
curl http://127.0.0.1:9901/ready

# Send a request -- check for x-hello header
curl -si http://localhost:10000/some/path
```

Expected response headers include:

```
x-hello: from-luwes path=/some/path
```

Full response:

```
HTTP/1.1 200 OK
x-hello: from-luwes path=/some/path
content-length: 16
...

hello from luwes
```

**4. Try different paths**

```sh
curl -si http://localhost:10000/v1/chat/completions
# x-hello: from-luwes path=/v1/chat/completions

curl -si http://localhost:10000/
# x-hello: from-luwes path=/
```

## Filter structure

```
examples/hello/
  hello.go       -- Filter and Factory types, OnRequestHeaders, OnResponseHeaders
  cmd/main.go    -- Wiring: Register + RegisterHttpFilterConfigFactories
  envoy.yaml     -- Minimal Envoy config: one listener, direct_response backend
```

`cmd/main.go` is 13 lines. The composition layer (`sdk.Register`) eliminates
the per-filter boilerplate.

## Key patterns

**Copy before returning from OnRequestHeaders.** The `UnsafeEnvoyBuffer` returned
by `GetOne` points into Envoy-managed memory. That memory is valid only during the
callback. Calling `ToString()` copies it into Go memory before the callback returns.
Calling `ToUnsafeString()` and storing the result would be a use-after-free:

```go
// Correct: copy into Go memory
f.path = headers.GetOne(":path").ToString()

// Wrong: stores a pointer into Envoy memory that will be freed
f.path = headers.GetOne(":path").ToUnsafeString()  // dangling pointer after return
```

**Response headers in OnResponseHeaders, not OnRequestHeaders.** The response
header map does not exist at request time. Header mutation of the response must
happen in `OnResponseHeaders`.
