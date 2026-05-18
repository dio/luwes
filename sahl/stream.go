package sahl

import (
	"errors"
	"fmt"

	"github.com/dio/luwes/shared"
)

// HTTPStreamRequest carries parameters for initiating a bidirectional HTTP stream.
type HTTPStreamRequest struct {
	// Cluster is the Envoy cluster name. Required.
	Cluster string

	// Headers are the initial request headers, including HTTP/2 pseudo-headers
	// (:method, :path, :scheme, :authority) when required by the upstream.
	Headers [][2]string

	// Body is the initial request body chunk. Optional.
	Body []byte

	// EndOfStream marks the initial body as the final chunk. If false,
	// call stream.Send to deliver additional chunks.
	EndOfStream bool

	// TimeoutMs is the stream timeout in milliseconds. 0 means Envoy default.
	TimeoutMs uint64
}

// HTTPStreamEvent is a sealed interface delivered to the event fn on each
// upstream event. Concrete types: *HTTPStreamHeaders, *HTTPStreamData,
// *HTTPStreamTrailers, *HTTPStreamComplete, *HTTPStreamReset.
type HTTPStreamEvent interface{ httpStreamEvent() }

// HTTPStreamHeaders carries the upstream response headers.
type HTTPStreamHeaders struct {
	StreamID  uint64
	Headers   [][2]shared.UnsafeEnvoyBuffer
	EndStream bool
}

// HTTPStreamData carries a response body chunk.
type HTTPStreamData struct {
	StreamID  uint64
	Body      []shared.UnsafeEnvoyBuffer
	EndStream bool
}

// HTTPStreamTrailers carries the upstream response trailers.
type HTTPStreamTrailers struct {
	StreamID uint64
	Trailers [][2]shared.UnsafeEnvoyBuffer
}

// HTTPStreamComplete signals that the upstream stream finished cleanly.
type HTTPStreamComplete struct {
	StreamID uint64
}

// HTTPStreamReset signals that the upstream stream was reset.
type HTTPStreamReset struct {
	StreamID uint64
	Reason   shared.HttpStreamResetReason
}

func (*HTTPStreamHeaders) httpStreamEvent()  {}
func (*HTTPStreamData) httpStreamEvent()     {}
func (*HTTPStreamTrailers) httpStreamEvent() {}
func (*HTTPStreamComplete) httpStreamEvent() {}
func (*HTTPStreamReset) httpStreamEvent()    {}

// HTTPStreamEventFunc is the callback called on the Envoy worker thread for
// each upstream stream event.
type HTTPStreamEventFunc func(event HTTPStreamEvent)

// HTTPStream is a handle to a live HTTP stream. Use it to send additional
// request body chunks after w.HTTPStream returns.
type HTTPStream struct {
	streamID uint64
	handle   shared.HttpFilterHandle
}

// Send sends a body chunk on the stream. endStream=true closes the request side.
func (s *HTTPStream) Send(body []byte, endStream bool) {
	s.handle.SendHttpStreamData(s.streamID, body, endStream)
}

// SendTrailers sends request trailers and closes the request side.
func (s *HTTPStream) SendTrailers(trailers [][2]string) {
	s.handle.SendHttpStreamTrailers(s.streamID, trailers)
}

// Reset cancels the stream immediately.
func (s *HTTPStream) Reset() {
	s.handle.ResetHttpStream(s.streamID)
}

// errStreamInitResult maps HttpCalloutInitResult to an error for stream init failures.
func errStreamInitResult(r shared.HttpCalloutInitResult) error {
	switch r {
	case shared.HttpCalloutInitSuccess:
		return nil
	case shared.HttpCalloutInitMissingRequiredHeaders:
		return errors.New("sahl: HTTPStream: missing required headers")
	case shared.HttpCalloutInitClusterNotFound:
		return errors.New("sahl: HTTPStream: cluster not found")
	case shared.HttpCalloutInitDuplicateCalloutId:
		return errors.New("sahl: HTTPStream: duplicate stream ID")
	case shared.HttpCalloutInitCannotCreateRequest:
		return errors.New("sahl: HTTPStream: cannot create request")
	default:
		return fmt.Errorf("sahl: HTTPStream: unknown init result %d", r)
	}
}

// HTTPStream initiates a bidirectional HTTP stream to the given cluster.
// The handler must return immediately after calling HTTPStream; Envoy pauses
// the request (HeadersStatusStop is returned from OnRequestHeaders).
// fn is called on the Envoy worker thread for each upstream event.
//
// Returns an HTTPStream handle for sending additional request body chunks.
// Returns (nil, err) on init failure; fn is never called in that case.
//
// HTTPStream panics if a stream or callout is already in flight on this request,
// or if w.Go was already called.
func (w *Writer) HTTPStream(req HTTPStreamRequest, fn HTTPStreamEventFunc) (*HTTPStream, error) {
	if w.streamStarted {
		panic("BUG: sahl: HTTPStream called twice on the same request")
	}
	if w.calloutStarted {
		panic("BUG: sahl: HTTPStream and HTTPCallout are mutually exclusive on the same request")
	}
	if w.goStarted {
		panic("BUG: sahl: HTTPStream and Go are mutually exclusive on the same request")
	}

	// Store event fn before initiating: Envoy may fire callbacks inline.
	w.streamEventFn = fn
	init, streamID := w.handle.StartHttpStream(
		req.Cluster, req.Headers, req.Body, req.EndOfStream, req.TimeoutMs, w.streamCB,
	)
	if init != shared.HttpCalloutInitSuccess {
		w.streamEventFn = nil
		return nil, errStreamInitResult(init)
	}
	w.streamStarted = true
	return &HTTPStream{streamID: streamID, handle: w.handle}, nil
}
