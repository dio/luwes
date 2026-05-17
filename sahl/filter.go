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

	handler := f.def.handler

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
		handler = fn

	case f.def.configFn != nil:
		if err := f.def.configFn(ch); err != nil {
			h.Log(shared.LogLevelError, "sahl: filter %q config failed: %v", f.name, err)
			return nil, err
		}
	}

	if handler == nil {
		err := fmt.Errorf("sahl: filter %q has nil handler", f.name)
		h.Log(shared.LogLevelError, "%v", err)
		return nil, err
	}

	return &filterFactory{name: f.name, handler: handler}, nil
}

func (f *configFactory) CreatePerRoute(_ []byte) (any, error) { return nil, nil }

// -- filterFactory: shared.HttpFilterFactory --

type filterFactory struct {
	name    string
	handler HandlerFunc
}

func (f *filterFactory) Create(handle shared.HttpFilterHandle) shared.HttpFilter {
	return newSahlFilter(f.name, f.handler, handle)
}

func (f *filterFactory) OnDestroy() {}

// -- sahlFilter: shared.HttpFilter --

type sahlFilter struct {
	shared.EmptyHttpFilter

	name    string
	handler HandlerFunc
	handle  shared.HttpFilterHandle

	req    *Request
	writer *Writer

	// body buffering state for r.Body()
	bodyDone bool
}

var filterPool = sync.Pool{New: func() any { return &sahlFilter{} }}

func newSahlFilter(name string, handler HandlerFunc, handle shared.HttpFilterHandle) *sahlFilter {
	f := filterPool.Get().(*sahlFilter)
	if f.handler != nil {
		panic("BUG: sahl: filter pool corruption: handler still set at reuse")
	}
	f.name = name
	f.handler = handler
	f.handle = handle
	f.bodyDone = false
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
	f.writer = w

	// Run the handler synchronously on the worker thread.
	f.handler(w, req)

	if w.goStarted {
		// Handler called w.Go(): goroutine is running, worker thread released.
		// Envoy will call ContinueRequest from the scheduler callback in flush().
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
	return shared.BodyStatusStopAndBuffer
}

func (f *sahlFilter) OnStreamComplete() {
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
	f.handler = nil // zero before pool return so assertion catches leaks
	filterPool.Put(f)
}
