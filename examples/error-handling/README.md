# error-handling

Reference filter for observing and handling errors across the full
request/response path in a luwes filter. Built on raw luwes (no sahl layer)
to expose the complete error API surface.

## What it demonstrates

Five error paths, each with its own counter and log line:

| Path | Trigger | Response | Counter |
|------|---------|----------|---------|
| Callout init failure | cluster not found, missing headers | 503 | `error_handling_callout_init_fail{reason}` |
| Callout network failure | upstream reset, buffer overflow | 502 | `error_handling_callout_net_fail{result}` |
| Callout upstream error | callout returns 4xx/5xx | 401 or 403 | `error_handling_callout_upstream_err{status}` |
| Envoy local reply | upstream timeout, circuit breaker, rate limit | pass-through + `x-error-details` header | `error_handling_local_reply{code}` |
| Response flags | upstream failure, downstream reset | logged in OnStreamComplete | `error_handling_response_flags{flags}` |

## Error paths in detail

### 1. Callout init failure (503)

Happens before the callout is even sent: the cluster name does not exist in
Envoy's CDS, or a required header is missing from the callout request.
`HttpCallout` returns a non-success `HttpCalloutInitResult` synchronously.
The filter detects this at call time and calls `SendLocalResponse(503)` before
returning from `OnRequestHeaders`.

```go
init, _ := handle.HttpCallout("auth", headers, nil, 500, f)
if init != shared.HttpCalloutInitSuccess {
    handle.SendLocalResponse(503, nil, body, "callout_init_fail")
    return shared.HeadersStatusStop
}
```

The callout callback (`OnHttpCalloutDone`) is never called on init failure.

### 2. Callout network failure (502)

The callout was initiated successfully but the connection to the auth cluster
failed or was reset before a response arrived. Envoy fires `OnHttpCalloutDone`
with `result != HttpCalloutSuccess`:

| Result | Meaning |
|--------|---------|
| `HttpCalloutReset` | upstream reset the connection |
| `HttpCalloutExceedResponseBufferLimit` | response body larger than Envoy's buffer limit |

```go
func (f *Filter) OnHttpCalloutDone(_ uint64, result shared.HttpCalloutResult, ...) {
    if result != shared.HttpCalloutSuccess {
        handle.SendLocalResponse(502, nil, body, "callout_net_fail")
        return
    }
    // ...
}
```

### 3. Callout upstream error (401 / 403)

The callout completed and Envoy delivered a response, but the auth service
returned a non-2xx status. The filter reads `:status` from the callout response
headers and decides how to respond downstream:

- `401` from auth: forward 401 (the client's credentials are invalid).
- Any other non-2xx: send 403 (hide internal error codes from the client).

The callout body is not read: only the status header matters here.

### 4. Envoy local reply (OnLocalReply)

Envoy itself generates a local reply when an upstream timeout fires, a circuit
breaker opens, a rate limiter trips, or a buffer overflows. This is distinct
from the filter calling `SendLocalResponse` directly.

`OnLocalReply` is called with the response code and a `details` string that
identifies the reason (e.g. `upstream_reset_before_response_started`,
`response_timeout`). The filter:

1. Emits `error_handling_local_reply{code}`.
2. Adds `x-error-details: <details>` to the response so the client can
   distinguish an Envoy-level error from a real upstream response.
3. Returns `LocalReplyStatusContinue` to let the local reply proceed.
4. Skips the header mutation when `resetImminent=true` (no headers will be sent).

`ResponseHeaders()` is available inside `OnLocalReply`. You can modify the
response headers here; they will be sent as part of the local reply.

Note: `OnLocalReply` is NOT called for replies the filter generates via its
own `SendLocalResponse`. It is only called for Envoy-initiated local replies.

### 5. Response flags (OnStreamComplete)

After every request, `OnStreamComplete` reads `AttributeIDResponseFlags` via
`GetAttributeString`. The flags string is a comma-separated set of Envoy
response flags that describe how the request ended:

| Flag | Meaning |
|------|---------|
| `UF` | Upstream connection failure |
| `UH` | No healthy upstream hosts |
| `UC` | Upstream connection termination |
| `UT` | Upstream request timeout |
| `DC` | Downstream connection termination (client disconnected mid-request) |
| `RL` | Rate limited |
| `UO` | Upstream overflow (circuit breaker) |

The filter emits `error_handling_response_flags{flags}` and logs at Info when
flags are non-empty. This ties the flag to the specific request path and
timestamp visible in the access log.

`GetAttributeString` on `AttributeIDResponseFlags` returns an empty buffer for
requests that complete successfully (no flags set).

## Prerequisites

- Go 1.22+ with CGO enabled
- Envoy is downloaded automatically by `make run`
- A mock auth server and upstream (see below)

## Make targets

```sh
# Build the .so
make build EXAMPLE=error-handling

# Start Envoy (foreground, Ctrl-C to stop)
make run EXAMPLE=error-handling

# Run unit tests (no Envoy required)
make examples/test/examples/error-handling
```

## Manual steps

**1. Build**

```sh
CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
  -o dist/liberror-handling.so ./examples/error-handling/cmd
```

**2. Start mock servers**

The filter makes a callout to `127.0.0.1:11000` (auth cluster) and forwards
accepted requests to `127.0.0.1:11001` (upstream cluster).

Auth mock (responds based on path):

```sh
python3 -c "
import http.server

class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == '/deny':
            self.send_response(401)
            self.end_headers()
        elif self.path == '/error':
            self.send_response(500)
            self.end_headers()
        else:
            self.send_response(200)
            self.send_header('x-auth-user', 'alice')
            self.end_headers()
    def log_message(self, *a): pass

http.server.HTTPServer(('127.0.0.1', 11000), H).serve_forever()
" &
```

Upstream mock (echoes the request):

```sh
python3 -c "
import http.server

class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        user = self.headers.get('x-auth-user', '')
        self.send_response(200)
        self.end_headers()
        self.wfile.write(('ok user=' + user + '\n').encode())
    def log_message(self, *a): pass

http.server.HTTPServer(('127.0.0.1', 11001), H).serve_forever()
" &
```

**3. Start Envoy**

```sh
make run EXAMPLE=error-handling
# or manually:
GODEBUG=cgocheck=0 \
ENVOY_DYNAMIC_MODULES_SEARCH_PATH=$(pwd)/dist \
.bin/envoy -c examples/error-handling/envoy.yaml --log-level warning
```

**4. Exercise each error path**

```sh
# Path 1 (callout init failure): stop the auth mock and restart Envoy so the
# cluster exists but the host is unreachable, or misconfigure envoy.yaml.
# With no auth mock running:
curl -si http://localhost:10000/api/test
# -> 503 {"error":"auth unavailable","reason":"cluster_not_found"}

# Path 2 (callout network failure): stop the auth mock after Envoy starts.
pkill -f "11000" 2>/dev/null  # stop auth mock
curl -si http://localhost:10000/api/reset
# -> 502 {"error":"auth unreachable"}

# Path 3a (upstream 401): restart auth mock, request the deny path.
curl -si http://localhost:10000/deny
# -> 401 {"error":"auth denied","upstream_status":"401"}

# Path 3b (upstream 5xx): auth mock returns 500 for /error.
curl -si http://localhost:10000/error
# -> 403 {"error":"auth denied","upstream_status":"500"}

# Happy path: auth returns 200 with x-auth-user.
curl -si http://localhost:10000/ok
# -> 200 ok user=alice
# Access log shows: user=alice

# Path 4 (local reply): configure the upstream cluster with a short timeout
# and make the upstream mock sleep. Envoy sends a local 504 and OnLocalReply
# fires. Check the response for x-error-details.
curl -si http://localhost:10000/ok
# -> 504 (if upstream mock sleeps past the 2s timeout in envoy.yaml)
# Headers include: x-error-details: response_timeout
```

**5. Check metrics**

```sh
curl -s http://localhost:9901/stats | grep error_handling
```

Expected after exercising all paths:

```
dynamicmodulescustom.error_handling_callout_init_fail.cluster_not_found: 1
dynamicmodulescustom.error_handling_callout_net_fail.reset: 1
dynamicmodulescustom.error_handling_callout_upstream_err.401: 1
dynamicmodulescustom.error_handling_callout_upstream_err.500: 1
dynamicmodulescustom.error_handling_local_reply.504: 1
```

## Filter structure

```
examples/error-handling/
  error_handling.go      Filter, Factory, all five error path handlers
  error_handling_test.go 9 unit tests covering every error path (no Envoy required)
  cmd/main.go            wiring: Register + RegisterHttpFilterConfigFactories
  envoy.yaml             two clusters: auth (callout) + upstream (backend)
```

## Key patterns

**Implement HttpCalloutCallback on the Filter struct.** The raw luwes SDK requires
the callout callback to be a `shared.HttpCalloutCallback` interface. Embedding it
on the `Filter` struct (alongside `EmptyHttpFilter`) means `f` satisfies both
interfaces and no extra allocation is needed to pass the callback.

**Check `HttpCalloutInitResult` before returning.** `HttpCallout` can fail
synchronously. If you ignore the init result and return `HeadersStatusStop`, the
request stalls forever: Envoy is paused waiting for `ContinueRequest`, but the
callback never fires because the callout was never initiated. Always check the
result and call `SendLocalResponse` on failure.

**`OnLocalReply` is for observation, not interception.** You can read and modify
response headers, emit metrics, and log. You cannot prevent the local reply from
being sent -- `LocalReplyStatusContinue` and `LocalReplyStatusContinueAndResetStream`
both send a reply. Use `resetImminent` to skip header mutations that would be wasted
(the TCP connection is going away).

**Response flags in `OnStreamComplete`.** `GetAttributeString(AttributeIDResponseFlags)`
returns the final flags string after the request is fully resolved -- upstream
result, downstream state, and all filter decisions accounted for. This is the
only place to observe `DC` (downstream connection termination) and correlate it
with a specific request path and trace ID.

**`itoa` without `strconv`.** The filter uses a local `itoa` to convert status
codes to strings for metric tags. `strconv.Itoa` is correct but allocates for
values not in its internal string cache. For a bounded set of known HTTP status
codes, a simple loop into a stack buffer is zero-alloc.
