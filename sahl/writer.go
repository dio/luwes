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
	metaMuts    []metaMut
	counterMuts []counterMut

	responded    bool // Send/SendBytes was called
	routeCleared bool

	// Go() state
	goStarted bool
	goCtx     context.Context
	goCancel  context.CancelFunc
}

type headerMut struct{ key, value string }
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
	w.metaMuts = w.metaMuts[:0]
	w.counterMuts = w.counterMuts[:0]
	w.responded = false
	w.routeCleared = false
	w.goStarted = false
	w.goCtx = nil
	w.goCancel = nil
}

// Send sends a local HTTP response to the downstream client with the given
// status code and string body. The request is not forwarded upstream.
// Must be called at most once per request.
func (w *Writer) Send(statusCode int, body string) {
	w.SendBytes(statusCode, []byte(body))
}

// SendBytes is like Send but accepts a pre-encoded byte slice.
func (w *Writer) SendBytes(statusCode int, body []byte) {
	if w.responded {
		return
	}
	w.responded = true
	w.handle.SendLocalResponse(uint32(statusCode), nil, body, "sahl")
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

// flush applies all queued mutations.
// continueReq must be true only when called from a Go() goroutine via
// Scheduler.Schedule: in that case Envoy is paused (HeadersStatusStop was
// returned) and ContinueRequest must be called to resume processing.
// For synchronous handlers, the caller returns HeadersStatusContinue directly;
// calling ContinueRequest here too would double-continue and corrupt state.
func (w *Writer) flush(continueReq bool) {
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
