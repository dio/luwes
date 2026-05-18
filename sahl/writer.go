package sahl

import (
	"context"
	"sync"

	"github.com/dio/luwes/shared"
)

// Writer queues mutations to be applied on the Envoy worker thread after
// the handler returns. All methods are safe to call from a [Writer.Go] goroutine.
type Writer struct {
	handle    shared.HttpFilterHandle
	scheduler shared.Scheduler // acquired once in OnRequestHeaders, used in Go()

	// queued mutations: slices retain capacity across pool reuse
	reqMuts     []headerMut
	respHdrMuts []headerMut // response headers sent with SendBytes/Send
	metaMuts    []metaMut
	counterMuts []counterMut

	responded    bool // Send/SendBytes was called
	routeCleared bool
	localResp    *localResponseMut // queued local response, applied in flush

	// Go() state
	goStarted bool
	goCtx     context.Context
	goCancel  context.CancelFunc

	// HTTPCallout() state
	calloutStarted bool
	calloutFn      HTTPCalloutFunc
	// calloutCB is set by sahlFilter before w is handed to the handler.
	// It holds a back-reference so OnHttpCalloutDone can flush after fn runs.
	calloutCB shared.HttpCalloutCallback

	// HTTPStream() state
	streamStarted bool
	streamEventFn HTTPStreamEventFunc
	// streamCB is set by sahlFilter before w is handed to the handler.
	streamCB shared.HttpStreamCallback
}

type headerMut struct{ key, value string }
type localResponseMut struct {
	statusCode uint32
	headers    [][2]string
	body       []byte
}
type metaMut struct {
	namespace, key string
	value          any
}

type counterMut struct {
	id   shared.MetricID
	n    uint64
	tags []string
	hist bool // false = counter, true = histogram
}

var writerPool = sync.Pool{New: func() any { return &Writer{} }}

func getWriter(handle shared.HttpFilterHandle, scheduler shared.Scheduler) *Writer {
	w := writerPool.Get().(*Writer)
	w.reset(handle, scheduler)
	return w
}

func putWriter(w *Writer) {
	writerPool.Put(w)
}

func (w *Writer) reset(handle shared.HttpFilterHandle, scheduler shared.Scheduler) {
	w.handle = handle
	w.scheduler = scheduler
	w.reqMuts = w.reqMuts[:0]
	w.respHdrMuts = w.respHdrMuts[:0]
	w.metaMuts = w.metaMuts[:0]
	w.counterMuts = w.counterMuts[:0]
	w.responded = false
	w.routeCleared = false
	w.localResp = nil
	w.goStarted = false
	w.goCtx = nil
	w.goCancel = nil
	w.calloutStarted = false
	w.calloutFn = nil
	w.calloutCB = nil
	w.streamStarted = false
	w.streamEventFn = nil
	w.streamCB = nil
}

// Send sends a local HTTP response to the downstream client with the given
// status code and string body. The request is not forwarded upstream.
// Must be called at most once per request.
func (w *Writer) Send(statusCode int, body string) {
	w.SendBytes(statusCode, []byte(body))
}

// SendBytes is like Send but accepts a pre-encoded byte slice.
// Response headers queued via SetResponseHeader are included.
// Safe to call from inside w.Go: the actual SendLocalResponse is deferred
// to flush() which runs on the Envoy worker thread via Scheduler.Schedule.
func (w *Writer) SendBytes(statusCode int, body []byte) {
	if w.responded {
		return
	}
	w.responded = true
	var headers [][2]string
	if len(w.respHdrMuts) > 0 {
		headers = make([][2]string, len(w.respHdrMuts))
		for i, m := range w.respHdrMuts {
			headers[i] = [2]string{m.key, m.value}
		}
	}
	if w.goStarted {
		// Defer to flush(): we're inside a goroutine, handle calls must run
		// on the Envoy worker thread. flush() is called via Scheduler.Schedule.
		w.localResp = &localResponseMut{
			statusCode: uint32(statusCode),
			headers:    headers,
			body:       body,
		}
		return
	}
	w.handle.SendLocalResponse(uint32(statusCode), headers, body, "sahl")
}

// SetResponseHeader queues a response header to be included in the next
// Send or SendBytes call. Has no effect after Send/SendBytes is called.
// Use this to set content-type, cache-control, and other response headers
// when serving assets directly from a filter (e.g. embedded SPA, API handler).
func (w *Writer) SetResponseHeader(key, value string) {
	if !w.responded {
		w.respHdrMuts = append(w.respHdrMuts, headerMut{key, value})
	}
}

// Log emits a message to Envoy's logger at the given level.
func (w *Writer) Log(level shared.LogLevel, format string, args ...any) {
	w.handle.Log(level, format, args...)
}

// SetRequestHeader queues a mutation to set a request header before forwarding
// upstream. Has no effect if Send or SendBytes was called.
func (w *Writer) SetRequestHeader(key, value string) {
	if !w.responded {
		w.reqMuts = append(w.reqMuts, headerMut{key, value})
	}
}

// SetMetadata sets a dynamic metadata value on the stream, applied before
// ContinueRequest. value must be string, int64, float64, or bool.
// Has no effect if Send or SendBytes was called.
func (w *Writer) SetMetadata(namespace, key string, value any) {
	if !w.responded {
		w.metaMuts = append(w.metaMuts, metaMut{namespace, key, value})
	}
}

// ClearRouteCache clears Envoy's cached route for this stream. Call after
// setting a cluster-selection header (e.g. x-cluster) so Envoy re-evaluates
// the cluster_header route with the new value.
func (w *Writer) ClearRouteCache() {
	w.routeCleared = true
}

// IncrementCounter increments the counter metric by n. tags are tag values
// in the same order as the tag keys declared in DefineCounter.
func (w *Writer) IncrementCounter(id MetricID, n uint64, tags ...string) {
	w.counterMuts = append(w.counterMuts, counterMut{id: id, n: n, tags: tags})
}

// RecordHistogram records a histogram observation. tags are tag values in
// the same order as the tag keys declared in DefineHistogram.
func (w *Writer) RecordHistogram(id MetricID, n uint64, tags ...string) {
	w.counterMuts = append(w.counterMuts, counterMut{id: id, n: n, tags: tags, hist: true})
}

// Go upgrades this request to goroutine mode and runs fn in a new goroutine.
// The handler function should return immediately after calling Go.
//
// fn receives a context that is cancelled if the client disconnects
// (OnStreamComplete). Mutations queued on w inside fn are applied on the
// Envoy worker thread via Scheduler.Schedule when fn returns.
//
// Go must be called at most once per request. Panics on duplicate calls.
//
// For filters that never call Go, zero goroutines are spawned per request.
// Only requests that call Go pay the goroutine cost.
//
//	func myHandler(w *sahl.Writer, r *sahl.Request) {
//	    // Fast path: synchronous, zero goroutine.
//	    if v, ok := cache.Get(r.Path); ok {
//	        w.SetRequestHeader("x-cached", v)
//	        return
//	    }
//
//	    // Slow path: needs Redis.
//	    w.Go(func(ctx context.Context) {
//	        val, err := redis.Get(ctx, r.Path)
//	        if err != nil {
//	            w.Send(503, `{"error":"unavailable"}`)
//	            return
//	        }
//	        w.SetRequestHeader("x-value", val)
//	    })
//	}
func (w *Writer) Go(fn func(ctx context.Context)) {
	if w.goStarted {
		panic("BUG: sahl: Go called twice on the same request")
	}
	w.goStarted = true
	ctx, cancel := context.WithCancel(context.Background())
	w.goCtx = ctx
	w.goCancel = cancel
	scheduler := w.scheduler
	go func() {
		defer cancel()
		fn(ctx)
		// Hop back to the Envoy worker thread to flush mutations, then resume.
		scheduler.Schedule(func() {
			w.flush(true)
		})
	}()
}

// flushResponseMutations applies counter and metadata mutations queued by a
// response observer. Called after the final OnResponseBody chunk. Unlike
// flush(), it does not apply request header mutations or call ContinueRequest
// (the request has already been forwarded upstream at this point).
func (w *Writer) flushResponseMutations() {
	for _, m := range w.metaMuts {
		w.handle.SetMetadata(m.namespace, m.key, m.value)
	}
	for _, m := range w.counterMuts {
		if m.hist {
			w.handle.RecordHistogramValue(m.id, m.n, m.tags...)
		} else {
			w.handle.IncrementCounterValue(m.id, m.n, m.tags...)
		}
	}
	// Clear to avoid double-apply if flush() is called later (shouldn't happen,
	// but defensive).
	w.metaMuts = w.metaMuts[:0]
	w.counterMuts = w.counterMuts[:0]
}

// SetResponseBody replaces the entire buffered response body with newBody.
// Only valid inside a ResponseHandlerFunc when the filter is registered with
// mutable-response mode (RegisterWithMutableResponse). Has no effect otherwise.
// Must be called on the EndStream=true call; calling earlier is a no-op because
// Envoy has not buffered the complete body yet.
func (w *Writer) SetResponseBody(newBody []byte) {
	body := w.handle.BufferedResponseBody()
	if body == nil {
		return
	}
	body.Drain(body.GetSize())
	body.Append(newBody)
}

// AppendResponseBody appends data to the end of the buffered response body.
// Only valid inside a ResponseHandlerFunc when the filter is in mutable-response
// mode. Like SetResponseBody, best called on EndStream=true.
func (w *Writer) AppendResponseBody(data []byte) {
	body := w.handle.BufferedResponseBody()
	if body == nil {
		return
	}
	body.Append(data)
}

// flush applies all queued mutations.
// Scheduler.Schedule: in that case Envoy is paused (HeadersStatusStop was
// returned) and ContinueRequest must be called to resume processing.
// For synchronous handlers, the caller returns HeadersStatusContinue directly;
// calling ContinueRequest here too would double-continue and corrupt state.
func (w *Writer) flush(continueReq bool) {
	if w.localResp != nil {
		w.handle.SendLocalResponse(w.localResp.statusCode, w.localResp.headers, w.localResp.body, "sahl")
		return
	}
	if w.responded {
		return
	}
	reqHdrs := w.handle.RequestHeaders()
	for _, m := range w.reqMuts {
		reqHdrs.Set(m.key, m.value)
	}
	for _, m := range w.metaMuts {
		w.handle.SetMetadata(m.namespace, m.key, m.value)
	}
	if w.routeCleared {
		w.handle.ClearRouteCache()
	}
	for _, m := range w.counterMuts {
		if m.hist {
			w.handle.RecordHistogramValue(m.id, m.n, m.tags...)
		} else {
			w.handle.IncrementCounterValue(m.id, m.n, m.tags...)
		}
	}
	if continueReq {
		w.handle.ContinueRequest()
	}
}
