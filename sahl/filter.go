package sahl

import (
	"fmt"
	"sync"

	"github.com/dio/luwes/shared"
)

// -- configFactory: shared.HttpFilterConfigFactory --

type configFactory struct {
	name string
	def  *filterDef
}

func newConfigFactory(name string, def *filterDef) *configFactory {
	return &configFactory{name: name, def: def}
}

func (f *configFactory) Create(
	h shared.HttpFilterConfigHandle,
	raw []byte,
) (shared.HttpFilterFactory, error) {
	ch := &configHandleImpl{h: h, raw: raw}

	// Resolve the handler: factory path produces a new HandlerFunc per config instance.
	def := &filterDef{
		handler:         f.def.handler,
		responseFn:      f.def.responseFn,
		bodyAware:       f.def.bodyAware,
		mutableResponse: f.def.mutableResponse,
	}

	switch {
	case f.def.factoryFn != nil:
		fn, err := f.def.factoryFn(ch)
		if err != nil {
			h.Log(shared.LogLevelError, "sahl: filter %q factory failed: %v", f.name, err)
			return nil, err
		}
		if fn == nil {
			err := fmt.Errorf("sahl: filter %q factory returned nil handler", f.name)
			h.Log(shared.LogLevelError, "%v", err)
			return nil, err
		}
		def.handler = fn

	case f.def.configFn != nil:
		if err := f.def.configFn(ch); err != nil {
			h.Log(shared.LogLevelError, "sahl: filter %q config failed: %v", f.name, err)
			return nil, err
		}
	}

	if def.handler == nil {
		panic(fmt.Sprintf("BUG: sahl: filter %q registered with nil handler", f.name))
	}

	return &filterFactory{name: f.name, def: def}, nil
}

func (f *configFactory) CreatePerRoute(_ []byte) (any, error) { return nil, nil }

// -- filterFactory: shared.HttpFilterFactory --

type filterFactory struct {
	name string
	def  *filterDef
}

func (f *filterFactory) Create(handle shared.HttpFilterHandle) shared.HttpFilter {
	return newSahlFilter(f.name, f.def, handle)
}

func (f *filterFactory) OnDestroy() {}

// -- sahlFilter: shared.HttpFilter --

// sahlFilter is per-request state.
type sahlFilter struct {
	shared.EmptyHttpFilter

	name    string
	handler *filterDef
	handle  shared.HttpFilterHandle

	req    *Request
	writer *Writer

	// respState holds per-response state for response observation.
	respState responseState

	// body buffering state for r.Body()
	bodyDone bool

	// streamDone is set by OnStreamComplete. Guards OnHttpCalloutDone and
	// OnHttpStream* callbacks: if the downstream disconnected while a callout
	// or stream was in-flight, Envoy may fire these callbacks late. Calling
	// the continuation fn or ContinueRequest on a completed stream is unsafe.
	streamDone bool
}

var filterPool = sync.Pool{New: func() any { return &sahlFilter{} }}

func newSahlFilter(name string, def *filterDef, handle shared.HttpFilterHandle) *sahlFilter {
	f := filterPool.Get().(*sahlFilter)
	if f.handler != nil {
		panic("BUG: sahl: filter pool corruption: handler still set at reuse")
	}
	f.name = name
	f.handler = def
	f.handle = handle
	f.bodyDone = false
	f.streamDone = false
	return f
}

var requestPool = sync.Pool{New: func() any { return &Request{} }}

func getRequest() *Request {
	return requestPool.Get().(*Request)
}

func putRequest(r *Request) {
	requestPool.Put(r)
}

func (f *sahlFilter) OnRequestHeaders(headers shared.HeaderMap, _ bool) shared.HeadersStatus {
	// Acquire scheduler before anything else: must be on worker thread.
	scheduler := f.handle.GetScheduler()

	// Pull pooled Request and Writer.
	req := getRequest()
	req.reset(headers, f.handle, f.name)
	f.req = req

	w := getWriter(f.handle, scheduler)
	w.calloutCB = f // sahlFilter implements shared.HttpCalloutCallback
	w.streamCB = f  // sahlFilter implements shared.HttpStreamCallback
	f.writer = w

	// Body-aware filters defer handler execution to OnRequestBody(endStream=true).
	// Return Stop here so Envoy buffers the body; handler runs when body is ready.
	if f.handler.bodyAware {
		return shared.HeadersStatusStopAllAndBuffer
	}

	// Run the request handler synchronously on the worker thread.
	f.handler.handler(w, req)

	if w.goStarted || w.calloutStarted || w.streamStarted {
		// Handler called w.Go(), w.HTTPCallout(), or w.HTTPStream(): worker thread released.
		// Envoy will call ContinueRequest from the scheduler/callout/stream callback in flush().
		return shared.HeadersStatusStop
	}

	// Synchronous handler: flush mutations now, on the worker thread.
	// Do NOT call ContinueRequest: returning Continue drives Envoy forward.
	if !w.responded {
		w.flush(false)
		return shared.HeadersStatusContinue
	}
	// Send/SendBytes was called: Envoy handles the response, return Stop to halt chain.
	return shared.HeadersStatusStop
}

func (f *sahlFilter) OnRequestBody(body shared.BodyBuffer, endStream bool) shared.BodyStatus {
	if endStream {
		f.bodyDone = true
	}
	if f.handler.bodyAware && endStream {
		// Body is fully buffered. Run the handler now, then continue upstream.
		f.handler.handler(f.writer, f.req)
		if !f.writer.responded {
			f.writer.flush(false)
			return shared.BodyStatusContinue
		}
		return shared.BodyStatusContinue
	}
	return shared.BodyStatusStopAndBuffer
}

func (f *sahlFilter) OnResponseHeaders(headers shared.HeaderMap, _ bool) shared.HeadersStatus {
	if f.handler.responseFn != nil {
		f.onResponseHeaders(headers)
	}
	return shared.HeadersStatusContinue
}

func (f *sahlFilter) OnResponseBody(body shared.BodyBuffer, endStream bool) shared.BodyStatus {
	if f.handler.responseFn != nil {
		f.onResponseBody(body, endStream)
	}
	if f.handler.mutableResponse {
		// Mutation mode: buffer all chunks until endStream, then let the
		// handler's SetResponseBody/AppendResponseBody calls apply to the
		// fully-buffered body before Envoy delivers it downstream.
		if !endStream {
			return shared.BodyStatusStopAndBuffer
		}
	}
	return shared.BodyStatusContinue
}

// OnHttpCalloutDone implements shared.HttpCalloutCallback.
// It runs on the Envoy worker thread when the callout response arrives.
func (f *sahlFilter) OnHttpCalloutDone(
	calloutID uint64,
	result shared.HttpCalloutResult,
	headers [][2]shared.UnsafeEnvoyBuffer,
	body []shared.UnsafeEnvoyBuffer,
) {
	// Guard: downstream may have disconnected while the callout was in-flight.
	// OnStreamComplete fires before this callback in that case. Skip fn and
	// flush to avoid calling ContinueRequest on a completed stream.
	if f.streamDone {
		return
	}
	w := f.writer
	if w == nil || w.calloutFn == nil {
		return
	}
	fn := w.calloutFn
	w.calloutFn = nil
	fn(result, headers, body)
	w.flush(true)
}

// OnHttpStreamHeaders implements shared.HttpStreamCallback.
func (f *sahlFilter) OnHttpStreamHeaders(streamID uint64, headers [][2]shared.UnsafeEnvoyBuffer, endStream bool) {
	w := f.writer
	if w == nil || w.streamEventFn == nil {
		return
	}
	w.streamEventFn(&HTTPStreamHeaders{StreamID: streamID, Headers: headers, EndStream: endStream})
}

// OnHttpStreamData implements shared.HttpStreamCallback.
func (f *sahlFilter) OnHttpStreamData(streamID uint64, body []shared.UnsafeEnvoyBuffer, endStream bool) {
	w := f.writer
	if w == nil || w.streamEventFn == nil {
		return
	}
	w.streamEventFn(&HTTPStreamData{StreamID: streamID, Body: body, EndStream: endStream})
}

// OnHttpStreamTrailers implements shared.HttpStreamCallback.
func (f *sahlFilter) OnHttpStreamTrailers(streamID uint64, trailers [][2]shared.UnsafeEnvoyBuffer) {
	w := f.writer
	if w == nil || w.streamEventFn == nil {
		return
	}
	w.streamEventFn(&HTTPStreamTrailers{StreamID: streamID, Trailers: trailers})
}

// OnHttpStreamComplete implements shared.HttpStreamCallback.
// Fires the Complete event, then flushes mutations and calls ContinueRequest.
func (f *sahlFilter) OnHttpStreamComplete(streamID uint64) {
	if f.streamDone {
		return
	}
	w := f.writer
	if w == nil {
		return
	}
	if w.streamEventFn != nil {
		w.streamEventFn(&HTTPStreamComplete{StreamID: streamID})
		w.streamEventFn = nil
	}
	w.flush(true)
}

// OnHttpStreamReset implements shared.HttpStreamCallback.
// Fires the Reset event. The event fn is responsible for calling w.Send if needed.
func (f *sahlFilter) OnHttpStreamReset(streamID uint64, reason shared.HttpStreamResetReason) {
	if f.streamDone {
		return
	}
	w := f.writer
	if w == nil || w.streamEventFn == nil {
		return
	}
	w.streamEventFn(&HTTPStreamReset{StreamID: streamID, Reason: reason})
	w.streamEventFn = nil
	// If the event fn did not call w.Send, flush normally so the request can continue
	// or the filter chain can proceed. If w.Send was called, flush is a no-op.
	w.flush(!w.responded)
}

func (f *sahlFilter) OnStreamComplete() {
	// Mark stream as done before anything else. Guards late-firing callout and
	// stream callbacks (OnHttpCalloutDone, OnHttpStreamComplete, OnHttpStreamReset)
	// that Envoy may deliver after the downstream has disconnected.
	f.streamDone = true
	// Flush response-side mutations (IncrementCounter, SetMetadata) queued by
	// the response observer. This is the guaranteed-last point where the handle
	// is still valid and the response body has been fully delivered.
	// Called even if endStream was never delivered (client disconnect, trailers).
	if f.handler != nil && f.handler.responseFn != nil && f.writer != nil {
		f.writer.flushResponseMutations()
	}

	// Cancel Go() goroutine context if one is running.
	if f.writer != nil && f.writer.goCancel != nil {
		f.writer.goCancel()
	}
}

func (f *sahlFilter) OnDestroy() {
	if f.req != nil {
		putRequest(f.req)
		f.req = nil
	}
	if f.writer != nil {
		putWriter(f.writer)
		f.writer = nil
	}
	f.respState.reset()
	f.handler = nil // zero before pool return so assertion catches leaks
	filterPool.Put(f)
}
