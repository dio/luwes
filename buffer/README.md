# buffer

Zero-allocation stream buffering primitives for Envoy dynamic module filters.

## When to use

Any filter that needs to extract metadata from a streaming response body
(SSE, NDJSON, chunked JSON) without buffering the entire stream. The
head+tail pattern covers the common LLM case where relevant data appears
at the beginning and end of the stream.

## Types

### Ring

Fixed-size circular buffer that captures the **last** n bytes written.
When full, new writes overwrite the oldest data. Zero-allocation after
construction.

```go
rb := buffer.NewRing(64 * 1024)

// In OnResponseBody:
rb.WriteChunks(body.GetChunks())   // zero-copy from Envoy memory

// After endOfStream:
data := rb.Bytes()   // linearised in-place (three-reversal rotation, O(n), no alloc)
```

### HeadTail

Captures both the **first** headSize bytes and the **last** tailSize bytes
of a stream without retaining the middle. This is the idiomatic pattern for
LLM SSE tapping:

- Input tokens appear near the **start** (Anthropic `message_start`, OpenAI first usage chunk)
- Output tokens appear near the **end** (`message_delta`, final usage chunk)

```go
ht := buffer.NewHeadTail(8*1024, 64*1024)

// In ResponseHandlerFunc (sahl) or OnResponseBody (raw luwes):
if len(chunk.Data) > 0 {
    ht.Write(chunk.Data)
}
if chunk.EndStream {
    head := ht.Head()   // first 8 KB
    tail := ht.Tail()   // last 64 KB
    usage := extractTokenUsage(head, tail)
}
```

## WriteChunks vs Write

`WriteChunks([]shared.UnsafeEnvoyBuffer)` is the idiomatic luwes entry
point: it feeds directly from `BodyBuffer.GetChunks()` without a caller-side
loop. Each `UnsafeEnvoyBuffer` points into Envoy-owned memory; `WriteChunks`
copies the bytes into the ring so they survive past the current callback.

`Write([]byte)` is for pure-Go tests and non-luwes callers.

## Allocation

Both `Ring` and `HeadTail` allocate once at construction (`NewRing`,
`NewHeadTail`). After that: zero allocations per chunk. `Bytes()` and
`Tail()` linearise in-place via three-reversal rotation: O(n) time,
O(1) space, no allocation.

## Used by

- `sahl/examples/sse-tap`: SSE token extraction
- `sahl/examples/decoder`: SSE + JSON token tap with per-cluster routing
