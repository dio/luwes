// Package abi_impl is the CGO binding layer between Envoy's dynamic module ABI
// and the luwes Go SDK. It is the only package in luwes that imports "C".
//
// Everything here is internal to the module binary. Filter authors never import
// this package directly; they blank-import it from their cmd/main.go:
//
//	import _ "github.com/dio/luwes/abi_impl"
//
// That import triggers the package-level init() which registers the ABI version
// string and wires luwes into Envoy's filter lifecycle.
//
// # Structure
//
// The package is organized into four layers:
//
//  1. Conversion helpers: Go<->C type bridges (stringToModuleBuffer, etc.).
//     None of these allocate; they produce view structs over existing memory.
//
//  2. Handle types: dymHeaderMap, dymBodyBuffer, dymScheduler, dymSpan,
//     dymHttpFilterHandle, dymConfigHandle. Each wraps a C pointer provided by
//     Envoy and implements the corresponding shared.* interface.
//
//  3. Manager: a sharded concurrent map (manager[T]) that pins Go objects so
//     their addresses can be round-tripped through C as opaque pointers.
//
//  4. ABI callbacks: the //export functions that Envoy calls at well-defined
//     points in the filter lifecycle (config load, request, response, destroy).
//
// # Memory model
//
// UnsafeEnvoyBuffer values returned by header and body accessors point into
// Envoy-owned memory. They are valid only within the callback in which they are
// obtained. Filter code must not retain them past the callback boundary without
// copying the contents into Go-owned memory first.
//
// # CGO escape
//
// Any local variable whose address is passed to a C function escapes to the
// heap. CGO requires the GC to be able to pin the object, and the GC only
// pins heap objects. Stack-allocated locals are not eligible. This is the root
// cause of the valueView allocation in getSingleHeader. It is structural and
// cannot be fixed without an ABI change (see RATIONALE.md).
//
// # Handle pool
//
// dymHttpFilterHandle is pooled via sync.Pool. The pool return point is
// on_http_filter_destroy, the guaranteed-last callback for a filter instance.
// Returning the handle in on_http_filter_stream_complete is incorrect because
// on_http_filter_destroy can fire after stream_complete and will see a handle
// that has already been reassigned to a different request.
package abi_impl

/*
#cgo darwin LDFLAGS: -Wl,-undefined,dynamic_lookup
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include "../abi/abi.h"

typedef const envoy_dynamic_module_type_envoy_buffer* ConstEnvoyBufferPtr;
*/
import "C"
import (
	_ "embed"
	"fmt"
	"runtime"
	"strconv"
	"sync"
	"unsafe"

	sdk "github.com/dio/luwes"
	"github.com/dio/luwes/shared"
)

type httpFilterConfigWrapper struct {
	pluginFactory shared.HttpFilterFactory
	configHandle  *dymConfigHandle
}

type httpFilterConfigWrapperPerRoute struct {
	config any
}

type httpFilterWrapper = dymHttpFilterHandle

type httpFilterSharedDataWrapper struct {
	data any
}

const numManagerShards = 32

// manager is a sharded concurrent map that pins Go objects so their addresses
// can be safely passed through C as opaque pointers. Sharding on the pointer
// value reduces lock contention across Envoy worker threads.
//
// T is one of the wrapper types (httpFilterConfigWrapper, httpFilterWrapper,
// etc.). The manager never owns the objects; callers allocate and the manager
// only records the pointer until remove() is called.
type manager[T any] struct {
	data  [numManagerShards]map[uintptr]*T
	mutex [numManagerShards]sync.Mutex
}

func (m *manager[T]) record(item *T) unsafe.Pointer {
	pointer := unsafe.Pointer(item)
	index := uintptr(pointer) % numManagerShards
	m.mutex[index].Lock()
	defer m.mutex[index].Unlock()
	// Assume the map is initialized.
	m.data[index][uintptr(pointer)] = item
	return pointer
}

func (m *manager[T]) unwrap(itemPtr unsafe.Pointer) *T {
	return (*T)(itemPtr)
}

func (m *manager[T]) search(key uintptr) *T {
	index := key % numManagerShards
	m.mutex[index].Lock()
	defer m.mutex[index].Unlock()
	return m.data[index][key]
}

func (m *manager[T]) remove(itemPtr unsafe.Pointer) {
	index := uintptr(itemPtr) % numManagerShards
	m.mutex[index].Lock()
	defer m.mutex[index].Unlock()
	delete(m.data[index], uintptr(itemPtr))
}

func newManager[T any]() *manager[T] {
	m := &manager[T]{}
	for i := 0; i < numManagerShards; i++ {
		m.data[i] = make(map[uintptr]*T)
	}
	return m
}

var (
	// configManager tracks active HttpFilterConfigWrapper instances, keyed by
	// the pointer passed to Envoy as http_filter_config_module_ptr.
	configManager = newManager[httpFilterConfigWrapper]()
	// configPerRouteManager tracks per-route config wrappers.
	configPerRouteManager = newManager[httpFilterConfigWrapperPerRoute]()
	// pluginManager tracks active dymHttpFilterHandle instances (one per
	// in-flight request), keyed by the pointer passed to Envoy as
	// http_filter_module_ptr.
	pluginManager = newManager[httpFilterWrapper]()
	// sharedDataManager tracks shared data wrappers for cross-filter state.
	sharedDataManager = newManager[httpFilterSharedDataWrapper]()
)

// Conversion helpers: zero-allocation Go<->C type bridges.
// All functions produce view structs over existing memory, no copies.
// Callers must ensure the source data outlives any C call that receives these
// views; use runtime.KeepAlive where the compiler cannot prove liveness.

func nullModuleBuffer() C.envoy_dynamic_module_type_module_buffer {
	return C.envoy_dynamic_module_type_module_buffer{
		ptr:    nil,
		length: 0,
	}
}

func stringToModuleBuffer(str string) C.envoy_dynamic_module_type_module_buffer {
	return C.envoy_dynamic_module_type_module_buffer{
		ptr:    (*C.char)(unsafe.Pointer(unsafe.StringData(str))),
		length: C.size_t(len(str)),
	}
}

func bytesToModuleBuffer(b []byte) C.envoy_dynamic_module_type_module_buffer {
	return C.envoy_dynamic_module_type_module_buffer{
		ptr:    (*C.char)(unsafe.Pointer(unsafe.SliceData(b))),
		length: C.size_t(len(b)),
	}
}

func stringArrayToModuleBufferSlice(
	strs []string,
) []C.envoy_dynamic_module_type_module_buffer {
	views := make([]C.envoy_dynamic_module_type_module_buffer, len(strs))
	for i, str := range strs {
		views[i] = stringToModuleBuffer(str)
	}
	return views
}

func headersToModuleHttpHeaderSlice(
	headers [][2]string,
) []C.envoy_dynamic_module_type_module_http_header {
	views := make([]C.envoy_dynamic_module_type_module_http_header, len(headers))
	for i, header := range headers {
		views[i] = C.envoy_dynamic_module_type_module_http_header{
			key_ptr:      (*C.char)(unsafe.Pointer(unsafe.StringData(header[0]))),
			key_length:   C.size_t(len(header[0])),
			value_ptr:    (*C.char)(unsafe.Pointer(unsafe.StringData(header[1]))),
			value_length: C.size_t(len(header[1])),
		}
	}
	return views
}

func envoyBufferToStringUnsafe(buf C.envoy_dynamic_module_type_envoy_buffer) string {
	return unsafe.String((*byte)(unsafe.Pointer(buf.ptr)), buf.length)
}

func envoyBufferToBytesUnsafe(buf C.envoy_dynamic_module_type_envoy_buffer) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(buf.ptr)), buf.length)
}

func envoyBufferToUnsafeEnvoyBuffer(buf C.envoy_dynamic_module_type_envoy_buffer) shared.UnsafeEnvoyBuffer {
	return shared.UnsafeEnvoyBuffer{
		Ptr: (*byte)(unsafe.Pointer(buf.ptr)),
		Len: uint64(buf.length),
	}
}

// envoyHttpHeaderSliceToUnsafeHeaderSlice converts a C header array (already
// fetched from Envoy) into a Go UnsafeEnvoyBuffer slice. Used by callout and
// stream callbacks where the C slice is provided by Envoy, not locally allocated.
func envoyHttpHeaderSliceToUnsafeHeaderSlice(
	buf []C.envoy_dynamic_module_type_envoy_http_header,
) [][2]shared.UnsafeEnvoyBuffer {
	headers := make([][2]shared.UnsafeEnvoyBuffer, len(buf))
	for i, header := range buf {
		headers[i] = [2]shared.UnsafeEnvoyBuffer{
			{Ptr: (*byte)(unsafe.Pointer(header.key_ptr)), Len: uint64(header.key_length)},
			{Ptr: (*byte)(unsafe.Pointer(header.value_ptr)), Len: uint64(header.value_length)},
		}
	}
	return headers
}

// envoyBufferSliceToUnsafeEnvoyBufferSlice converts a C buffer array (already
// fetched from Envoy) into a Go UnsafeEnvoyBuffer slice. Used by callout and
// stream callbacks where the C slice is provided by Envoy, not locally allocated.
func envoyBufferSliceToUnsafeEnvoyBufferSlice(
	buf []C.envoy_dynamic_module_type_envoy_buffer,
) []shared.UnsafeEnvoyBuffer {
	chunks := make([]shared.UnsafeEnvoyBuffer, 0, len(buf))
	for _, chunk := range buf {
		chunks = append(chunks, shared.UnsafeEnvoyBuffer{
			Ptr: (*byte)(unsafe.Pointer(chunk.ptr)),
			Len: uint64(chunk.length),
		})
	}
	return chunks
}

func hostLog(level shared.LogLevel, format string, args []any) {
	logLevel := uint32(level)
	// Quick check if logging is enabled at this level.
	if !bool(C.envoy_dynamic_module_callback_log_enabled(
		(C.envoy_dynamic_module_type_log_level)(logLevel),
	)) {
		return
	}
	message := fmt.Sprintf(format, args...)
	C.envoy_dynamic_module_callback_log(
		(C.envoy_dynamic_module_type_log_level)(logLevel),
		stringToModuleBuffer(message),
	)
	runtime.KeepAlive(message)
}

// dymHeaderMap implements shared.HeaderMap for a specific header phase
// (request headers, request trailers, response headers, response trailers).
// headerType is the ABI constant that identifies the phase; it is set once at
// handle initialization and never changes for the lifetime of the request.
//
// All reads return UnsafeEnvoyBuffer (Envoy-owned memory, valid only within
// the current callback. Do not retain these values past the callback boundary.
type dymHeaderMap struct {
	hostPluginPtr C.envoy_dynamic_module_type_http_filter_envoy_ptr
	headerType    C.envoy_dynamic_module_type_http_header_type
}

func (h *dymHeaderMap) getSingleHeader(key string, index uint64, valueCount *uint64) shared.UnsafeEnvoyBuffer {
	var valueView C.envoy_dynamic_module_type_envoy_buffer
	ret := C.envoy_dynamic_module_callback_http_get_header(
		h.hostPluginPtr,
		h.headerType,
		stringToModuleBuffer(key),
		&valueView,
		(C.size_t)(index),
		(*C.size_t)(valueCount),
	)

	if !bool(ret) || valueView.ptr == nil || valueView.length == 0 {
		return shared.UnsafeEnvoyBuffer{}
	}

	runtime.KeepAlive(key)
	return envoyBufferToUnsafeEnvoyBuffer(valueView)
}

func (h *dymHeaderMap) Get(key string) []shared.UnsafeEnvoyBuffer {
	valueCount := uint64(0)

	firstValue := h.getSingleHeader(key, 0, &valueCount)
	if valueCount == 0 {
		return nil // callers check len(result) > 0, so nil and empty slice are equivalent
	}

	// Single-value fast path: avoid make() for the common case.
	// HTTP headers with multiple values for the same key are rare.
	if valueCount == 1 {
		return []shared.UnsafeEnvoyBuffer{firstValue}
	}

	values := make([]shared.UnsafeEnvoyBuffer, 0, valueCount)
	values = append(values, firstValue)

	for i := uint64(1); i < valueCount; i++ {
		value := h.getSingleHeader(key, i, nil)
		values = append(values, value)
	}

	return values
}

func (h *dymHeaderMap) GetOne(key string) shared.UnsafeEnvoyBuffer {
	return h.getSingleHeader(key, 0, nil)
}

// GetOneInto writes the first value for key directly into out via an
// unsafe.Pointer cast, bypassing the local valueView declaration that
// getSingleHeader requires. The caller owns out; if it is stack-allocated
// (the common case), no heap allocation occurs on the hot path.
//
// Layout proof: UnsafeEnvoyBuffer and envoy_dynamic_module_type_envoy_buffer
// are both 16 bytes with ptr at offset 0 and length at offset 8 (verified at
// build time via the layout_check in the test suite).
func (h *dymHeaderMap) GetOneInto(key string, out *shared.UnsafeEnvoyBuffer) bool {
	cBuf := (*C.envoy_dynamic_module_type_envoy_buffer)(unsafe.Pointer(out))
	ret := C.envoy_dynamic_module_callback_http_get_header(
		h.hostPluginPtr,
		h.headerType,
		stringToModuleBuffer(key),
		cBuf,
		0,
		nil,
	)
	runtime.KeepAlive(key)
	return bool(ret) && out.Ptr != nil
}

func (h *dymHeaderMap) GetAll() [][2]shared.UnsafeEnvoyBuffer {
	headerCount := C.envoy_dynamic_module_callback_http_get_headers_size(
		(C.envoy_dynamic_module_type_http_filter_envoy_ptr)(h.hostPluginPtr),
		(C.envoy_dynamic_module_type_http_header_type)(h.headerType),
	)
	if headerCount == 0 {
		return nil
	}

	cHeaders := make([]C.envoy_dynamic_module_type_envoy_http_header, headerCount)
	C.envoy_dynamic_module_callback_http_get_headers(
		(C.envoy_dynamic_module_type_http_filter_envoy_ptr)(h.hostPluginPtr),
		(C.envoy_dynamic_module_type_http_header_type)(h.headerType),
		unsafe.SliceData(cHeaders),
	)
	result := make([][2]shared.UnsafeEnvoyBuffer, len(cHeaders))
	for i, hdr := range cHeaders {
		result[i] = [2]shared.UnsafeEnvoyBuffer{
			{Ptr: (*byte)(unsafe.Pointer(hdr.key_ptr)), Len: uint64(hdr.key_length)},
			{Ptr: (*byte)(unsafe.Pointer(hdr.value_ptr)), Len: uint64(hdr.value_length)},
		}
	}
	runtime.KeepAlive(cHeaders)
	return result
}

func (h *dymHeaderMap) Set(key, value string) {
	C.envoy_dynamic_module_callback_http_set_header(
		(C.envoy_dynamic_module_type_http_filter_envoy_ptr)(h.hostPluginPtr),
		(C.envoy_dynamic_module_type_http_header_type)(h.headerType),
		stringToModuleBuffer(key),
		stringToModuleBuffer(value),
	)
	runtime.KeepAlive(key)
	runtime.KeepAlive(value)
}

func (h *dymHeaderMap) Add(key, value string) {
	C.envoy_dynamic_module_callback_http_add_header(
		(C.envoy_dynamic_module_type_http_filter_envoy_ptr)(h.hostPluginPtr),
		(C.envoy_dynamic_module_type_http_header_type)(h.headerType),
		stringToModuleBuffer(key),
		stringToModuleBuffer(value),
	)
	runtime.KeepAlive(key)
	runtime.KeepAlive(value)
}

func (h *dymHeaderMap) Remove(key string) {
	// The ABI use the set to nil to remove the header.
	C.envoy_dynamic_module_callback_http_set_header(
		(C.envoy_dynamic_module_type_http_filter_envoy_ptr)(h.hostPluginPtr),
		(C.envoy_dynamic_module_type_http_header_type)(h.headerType),
		stringToModuleBuffer(key),
		nullModuleBuffer(),
	)
	runtime.KeepAlive(key)
}

// dymBodyBuffer implements shared.BodyBuffer for a specific body phase.
// bufferType distinguishes received vs. buffered body for both request and
// response. Like dymHeaderMap, it holds a view into Envoy-owned memory.
type dymBodyBuffer struct {
	hostPluginPtr C.envoy_dynamic_module_type_http_filter_envoy_ptr
	bufferType    C.envoy_dynamic_module_type_http_body_type
}

func (b *dymBodyBuffer) GetChunks() []shared.UnsafeEnvoyBuffer {
	size := C.envoy_dynamic_module_callback_http_get_body_chunks_size(
		(C.envoy_dynamic_module_type_http_filter_envoy_ptr)(b.hostPluginPtr),
		(C.envoy_dynamic_module_type_http_body_type)(b.bufferType),
	)
	if size == 0 {
		return nil
	}

	cChunks := make([]C.envoy_dynamic_module_type_envoy_buffer, size)
	C.envoy_dynamic_module_callback_http_get_body_chunks(
		(C.envoy_dynamic_module_type_http_filter_envoy_ptr)(b.hostPluginPtr),
		(C.envoy_dynamic_module_type_http_body_type)(b.bufferType),
		unsafe.SliceData(cChunks),
	)
	result := make([]shared.UnsafeEnvoyBuffer, len(cChunks))
	for i, chunk := range cChunks {
		result[i] = shared.UnsafeEnvoyBuffer{
			Ptr: (*byte)(unsafe.Pointer(chunk.ptr)),
			Len: uint64(chunk.length),
		}
	}
	runtime.KeepAlive(cChunks)
	return result
}

func (b *dymBodyBuffer) GetSize() uint64 {
	size := C.envoy_dynamic_module_callback_http_get_body_size(
		b.hostPluginPtr,
		b.bufferType,
	)
	return uint64(size)
}

func (b *dymBodyBuffer) Append(data []byte) {
	if len(data) == 0 {
		return
	}
	C.envoy_dynamic_module_callback_http_append_body(
		b.hostPluginPtr,
		b.bufferType,
		bytesToModuleBuffer(data),
	)
	runtime.KeepAlive(data)
}

func (b *dymBodyBuffer) Drain(size uint64) {
	C.envoy_dynamic_module_callback_http_drain_body(
		b.hostPluginPtr,
		b.bufferType,
		(C.size_t)(size),
	)
}

// dymScheduler implements shared.Scheduler. It bridges Envoy's callback-based
// scheduling (post a task ID to the host, receive it back on the worker thread)
// into a Go-friendly func() interface. The internal task map is protected by a
// mutex because Schedule() can be called from any goroutine, while onScheduled()
// is always called from the Envoy worker thread.
type dymScheduler struct {
	schedulerPtr  unsafe.Pointer
	schedulerLock sync.Mutex
	nextTaskID    uint64
	tasks         map[uint64]func()
	commitFunc    func(unsafe.Pointer, C.uint64_t)
}

func newDymScheduler(
	schedulerPtr unsafe.Pointer,
	commitFunc func(unsafe.Pointer, C.uint64_t),
) *dymScheduler {
	return &dymScheduler{
		schedulerPtr: schedulerPtr,
		tasks:        make(map[uint64]func()),
		commitFunc:   commitFunc,
	}
}

func (s *dymScheduler) Schedule(task func()) {
	// Lock the scheduler to prevent concurrent access
	s.schedulerLock.Lock()
	taskID := s.nextTaskID
	s.nextTaskID++
	s.tasks[taskID] = task
	s.schedulerLock.Unlock()

	// Call the host to schedule the task, passing the task ID as context
	s.commitFunc(s.schedulerPtr, C.uint64_t(taskID))
}

func (s *dymScheduler) onScheduled(taskID uint64) {
	s.schedulerLock.Lock()
	task := s.tasks[taskID]
	delete(s.tasks, taskID)
	s.schedulerLock.Unlock()
	if task != nil {
		task()
	}
}

type dymSpan struct {
	hostPluginPtr C.envoy_dynamic_module_type_http_filter_envoy_ptr
	spanPtr       C.envoy_dynamic_module_type_span_envoy_ptr
}

func (s *dymSpan) SetTag(key, value string) {
	if s == nil || s.spanPtr == nil {
		return
	}
	C.envoy_dynamic_module_callback_http_span_set_tag(
		s.spanPtr,
		stringToModuleBuffer(key),
		stringToModuleBuffer(value),
	)
	runtime.KeepAlive(key)
	runtime.KeepAlive(value)
}

func (s *dymSpan) SetOperation(operation string) {
	if s == nil || s.spanPtr == nil {
		return
	}
	C.envoy_dynamic_module_callback_http_span_set_operation(
		s.spanPtr,
		stringToModuleBuffer(operation),
	)
	runtime.KeepAlive(operation)
}

func (s *dymSpan) Log(event string) {
	if s == nil || s.spanPtr == nil {
		return
	}
	C.envoy_dynamic_module_callback_http_span_log(
		s.hostPluginPtr,
		s.spanPtr,
		stringToModuleBuffer(event),
	)
	runtime.KeepAlive(event)
}

func (s *dymSpan) SetSampled(sampled bool) {
	if s == nil || s.spanPtr == nil {
		return
	}
	C.envoy_dynamic_module_callback_http_span_set_sampled(s.spanPtr, C.bool(sampled))
}

func (s *dymSpan) GetBaggage(key string) (shared.UnsafeEnvoyBuffer, bool) {
	if s == nil || s.spanPtr == nil {
		return shared.UnsafeEnvoyBuffer{}, false
	}
	var valueView C.envoy_dynamic_module_type_envoy_buffer
	ret := C.envoy_dynamic_module_callback_http_span_get_baggage(
		s.spanPtr,
		stringToModuleBuffer(key),
		&valueView,
	)
	runtime.KeepAlive(key)
	if !bool(ret) {
		return shared.UnsafeEnvoyBuffer{}, false
	}
	if valueView.ptr == nil || valueView.length == 0 {
		return shared.UnsafeEnvoyBuffer{}, true
	}
	return envoyBufferToUnsafeEnvoyBuffer(valueView), true
}

func (s *dymSpan) SetBaggage(key, value string) {
	if s == nil || s.spanPtr == nil {
		return
	}
	C.envoy_dynamic_module_callback_http_span_set_baggage(
		s.spanPtr,
		stringToModuleBuffer(key),
		stringToModuleBuffer(value),
	)
	runtime.KeepAlive(key)
	runtime.KeepAlive(value)
}

func (s *dymSpan) GetTraceID() (shared.UnsafeEnvoyBuffer, bool) {
	if s == nil || s.spanPtr == nil {
		return shared.UnsafeEnvoyBuffer{}, false
	}
	var valueView C.envoy_dynamic_module_type_envoy_buffer
	ret := C.envoy_dynamic_module_callback_http_span_get_trace_id(s.spanPtr, &valueView)
	if !bool(ret) {
		return shared.UnsafeEnvoyBuffer{}, false
	}
	if valueView.ptr == nil || valueView.length == 0 {
		return shared.UnsafeEnvoyBuffer{}, true
	}
	return envoyBufferToUnsafeEnvoyBuffer(valueView), true
}

func (s *dymSpan) GetSpanID() (shared.UnsafeEnvoyBuffer, bool) {
	if s == nil || s.spanPtr == nil {
		return shared.UnsafeEnvoyBuffer{}, false
	}
	var valueView C.envoy_dynamic_module_type_envoy_buffer
	ret := C.envoy_dynamic_module_callback_http_span_get_span_id(s.spanPtr, &valueView)
	if !bool(ret) {
		return shared.UnsafeEnvoyBuffer{}, false
	}
	if valueView.ptr == nil || valueView.length == 0 {
		return shared.UnsafeEnvoyBuffer{}, true
	}
	return envoyBufferToUnsafeEnvoyBuffer(valueView), true
}

func (s *dymSpan) SpawnChild(operation string) shared.ChildSpan {
	if s == nil || s.spanPtr == nil {
		return nil
	}
	childPtr := C.envoy_dynamic_module_callback_http_span_spawn_child(
		s.hostPluginPtr,
		s.spanPtr,
		stringToModuleBuffer(operation),
	)
	runtime.KeepAlive(operation)
	if childPtr == nil {
		return nil
	}
	return &dymChildSpan{
		dymSpan: dymSpan{
			hostPluginPtr: s.hostPluginPtr,
			spanPtr:       C.envoy_dynamic_module_type_span_envoy_ptr(childPtr),
		},
		childPtr: childPtr,
	}
}

type dymChildSpan struct {
	dymSpan
	childPtr C.envoy_dynamic_module_type_child_span_module_ptr
}

func (s *dymChildSpan) Finish() {
	if s == nil || s.childPtr == nil {
		return
	}
	C.envoy_dynamic_module_callback_http_child_span_finish(s.childPtr)
	s.childPtr = nil
	s.spanPtr = nil
}

// dymHttpFilterHandle is the per-request handle. It implements
// shared.HttpFilterHandle and carries all per-request state: header maps, body
// buffers, the active filter instance, scheduler, callout callbacks, and stream
// callbacks.
//
// Instances are pooled in dymHttpFilterHandlePool. Pool return happens only in
// on_http_filter_destroy, never in on_http_filter_stream_complete. See the
// package doc and RATIONALE.md for why.
type dymHttpFilterHandle struct {
	hostPluginPtr C.envoy_dynamic_module_type_http_filter_envoy_ptr

	requestHeaderMap     dymHeaderMap
	responseHeaderMap    dymHeaderMap
	requestTrailerMap    dymHeaderMap
	responseTrailerMap   dymHeaderMap
	receivedRequestBody  dymBodyBuffer
	receivedResponseBody dymBodyBuffer
	bufferedRequestBody  dymBodyBuffer
	bufferedResponseBody dymBodyBuffer

	plugin            shared.HttpFilter
	scheduler         *dymScheduler
	streamCompleted   bool
	streamDestoried   bool
	localResponseSent bool
	// nextCalloutID was removed because callout ID is now returned by the host.

	calloutCallbacks map[uint64]shared.HttpCalloutCallback
	streamCallbacks  map[uint64]shared.HttpStreamCallback

	recordedSharedData []unsafe.Pointer

	downstreamWatermarkCallbacks shared.DownstreamWatermarkCallbacks
}

func (h *dymHttpFilterHandle) GetMetadataString(source shared.MetadataSourceType, metadataNamespace, key string) (shared.UnsafeEnvoyBuffer, bool) {
	var valueView C.envoy_dynamic_module_type_envoy_buffer

	ret := C.envoy_dynamic_module_callback_http_get_metadata_string(
		h.hostPluginPtr,
		(C.envoy_dynamic_module_type_metadata_source)(source),
		stringToModuleBuffer(metadataNamespace),
		stringToModuleBuffer(key),
		&valueView,
	)
	if !bool(ret) || valueView.ptr == nil || valueView.length == 0 {
		return shared.UnsafeEnvoyBuffer{}, false
	}

	runtime.KeepAlive(metadataNamespace)
	runtime.KeepAlive(key)
	return envoyBufferToUnsafeEnvoyBuffer(valueView), true
}

func (h *dymHttpFilterHandle) GetMetadataNumber(source shared.MetadataSourceType, metadataNamespace, key string) (float64, bool) {
	var value C.double = 0

	ret := C.envoy_dynamic_module_callback_http_get_metadata_number(
		h.hostPluginPtr,
		(C.envoy_dynamic_module_type_metadata_source)(source),
		stringToModuleBuffer(metadataNamespace),
		stringToModuleBuffer(key),
		&value,
	)
	if !bool(ret) {
		return 0, false
	}

	runtime.KeepAlive(metadataNamespace)
	runtime.KeepAlive(key)
	return float64(value), true
}

func (h *dymHttpFilterHandle) GetMetadataBool(source shared.MetadataSourceType, metadataNamespace, key string) (bool, bool) {
	var value C.bool

	ret := C.envoy_dynamic_module_callback_http_get_metadata_bool(
		h.hostPluginPtr,
		(C.envoy_dynamic_module_type_metadata_source)(source),
		stringToModuleBuffer(metadataNamespace),
		stringToModuleBuffer(key),
		&value,
	)
	if !bool(ret) {
		return false, false
	}

	runtime.KeepAlive(metadataNamespace)
	runtime.KeepAlive(key)
	return bool(value), true
}

func (h *dymHttpFilterHandle) GetMetadataKeys(source shared.MetadataSourceType, metadataNamespace string) []shared.UnsafeEnvoyBuffer {
	count := C.envoy_dynamic_module_callback_http_get_metadata_keys_count(
		h.hostPluginPtr,
		(C.envoy_dynamic_module_type_metadata_source)(source),
		stringToModuleBuffer(metadataNamespace),
	)
	if count == 0 {
		runtime.KeepAlive(metadataNamespace)
		return nil
	}

	buffers := make([]C.envoy_dynamic_module_type_envoy_buffer, int(count))
	ret := C.envoy_dynamic_module_callback_http_get_metadata_keys(
		h.hostPluginPtr,
		(C.envoy_dynamic_module_type_metadata_source)(source),
		stringToModuleBuffer(metadataNamespace),
		&buffers[0],
	)
	runtime.KeepAlive(metadataNamespace)
	if !bool(ret) {
		return nil
	}

	runtime.KeepAlive(buffers)
	return envoyBufferSliceToUnsafeEnvoyBufferSlice(buffers)
}

func (h *dymHttpFilterHandle) GetMetadataNamespaces(source shared.MetadataSourceType) []shared.UnsafeEnvoyBuffer {
	count := C.envoy_dynamic_module_callback_http_get_metadata_namespaces_count(
		h.hostPluginPtr,
		(C.envoy_dynamic_module_type_metadata_source)(source),
	)
	if count == 0 {
		return nil
	}

	buffers := make([]C.envoy_dynamic_module_type_envoy_buffer, int(count))
	ret := C.envoy_dynamic_module_callback_http_get_metadata_namespaces(
		h.hostPluginPtr,
		(C.envoy_dynamic_module_type_metadata_source)(source),
		&buffers[0],
	)
	if !bool(ret) {
		return nil
	}

	runtime.KeepAlive(buffers)
	return envoyBufferSliceToUnsafeEnvoyBufferSlice(buffers)
}

func (h *dymHttpFilterHandle) AddMetadataListNumber(metadataNamespace, key string, value float64) bool {
	ret := C.envoy_dynamic_module_callback_http_add_dynamic_metadata_list_number(
		h.hostPluginPtr,
		stringToModuleBuffer(metadataNamespace),
		stringToModuleBuffer(key),
		(C.double)(value),
	)
	runtime.KeepAlive(metadataNamespace)
	runtime.KeepAlive(key)
	return bool(ret)
}

func (h *dymHttpFilterHandle) AddMetadataListString(metadataNamespace, key string, value string) bool {
	ret := C.envoy_dynamic_module_callback_http_add_dynamic_metadata_list_string(
		h.hostPluginPtr,
		stringToModuleBuffer(metadataNamespace),
		stringToModuleBuffer(key),
		stringToModuleBuffer(value),
	)
	runtime.KeepAlive(metadataNamespace)
	runtime.KeepAlive(key)
	runtime.KeepAlive(value)
	return bool(ret)
}

func (h *dymHttpFilterHandle) AddMetadataListBool(metadataNamespace, key string, value bool) bool {
	ret := C.envoy_dynamic_module_callback_http_add_dynamic_metadata_list_bool(
		h.hostPluginPtr,
		stringToModuleBuffer(metadataNamespace),
		stringToModuleBuffer(key),
		(C.bool)(value),
	)
	runtime.KeepAlive(metadataNamespace)
	runtime.KeepAlive(key)
	return bool(ret)
}

func (h *dymHttpFilterHandle) GetMetadataListSize(source shared.MetadataSourceType, metadataNamespace, key string) (int, bool) {
	var result C.size_t = 0
	ret := C.envoy_dynamic_module_callback_http_get_metadata_list_size(
		h.hostPluginPtr,
		(C.envoy_dynamic_module_type_metadata_source)(source),
		stringToModuleBuffer(metadataNamespace),
		stringToModuleBuffer(key),
		&result,
	)
	runtime.KeepAlive(metadataNamespace)
	runtime.KeepAlive(key)
	if !bool(ret) {
		return 0, false
	}
	return int(result), true
}

func (h *dymHttpFilterHandle) GetMetadataListNumber(source shared.MetadataSourceType, metadataNamespace, key string, index int) (float64, bool) {
	var value C.double = 0
	ret := C.envoy_dynamic_module_callback_http_get_metadata_list_number(
		h.hostPluginPtr,
		(C.envoy_dynamic_module_type_metadata_source)(source),
		stringToModuleBuffer(metadataNamespace),
		stringToModuleBuffer(key),
		(C.size_t)(index),
		&value,
	)
	runtime.KeepAlive(metadataNamespace)
	runtime.KeepAlive(key)
	if !bool(ret) {
		return 0, false
	}
	return float64(value), true
}

func (h *dymHttpFilterHandle) GetMetadataListString(source shared.MetadataSourceType, metadataNamespace, key string, index int) (shared.UnsafeEnvoyBuffer, bool) {
	var valueView C.envoy_dynamic_module_type_envoy_buffer
	ret := C.envoy_dynamic_module_callback_http_get_metadata_list_string(
		h.hostPluginPtr,
		(C.envoy_dynamic_module_type_metadata_source)(source),
		stringToModuleBuffer(metadataNamespace),
		stringToModuleBuffer(key),
		(C.size_t)(index),
		&valueView,
	)
	runtime.KeepAlive(metadataNamespace)
	runtime.KeepAlive(key)
	if !bool(ret) {
		return shared.UnsafeEnvoyBuffer{}, false
	}
	// Handle the case where the value is empty string.
	if valueView.ptr == nil || valueView.length == 0 {
		return shared.UnsafeEnvoyBuffer{}, true
	}
	return envoyBufferToUnsafeEnvoyBuffer(valueView), true
}

func (h *dymHttpFilterHandle) GetMetadataListBool(source shared.MetadataSourceType, metadataNamespace, key string, index int) (bool, bool) {
	var value C.bool
	ret := C.envoy_dynamic_module_callback_http_get_metadata_list_bool(
		h.hostPluginPtr,
		(C.envoy_dynamic_module_type_metadata_source)(source),
		stringToModuleBuffer(metadataNamespace),
		stringToModuleBuffer(key),
		(C.size_t)(index),
		&value,
	)
	runtime.KeepAlive(metadataNamespace)
	runtime.KeepAlive(key)
	if !bool(ret) {
		return false, false
	}
	return bool(value), true
}

func (h *dymHttpFilterHandle) SetMetadata(metadataNamespace, key string, value any) {
	var numValue float64 = 0
	var isNum bool = false
	var strValue string = ""
	var isStr bool = false

	switch v := value.(type) {
	case uint:
		numValue = float64(v)
		isNum = true
	case uint8:
		numValue = float64(v)
		isNum = true
	case uint16:
		numValue = float64(v)
		isNum = true
	case uint32:
		numValue = float64(v)
		isNum = true
	case uint64:
		numValue = float64(v)
		isNum = true
	case int:
		numValue = float64(v)
		isNum = true
	case int8:
		numValue = float64(v)
		isNum = true
	case int16:
		numValue = float64(v)
		isNum = true
	case int32:
		numValue = float64(v)
		isNum = true
	case int64:
		numValue = float64(v)
		isNum = true
	case float32:
		numValue = float64(v)
		isNum = true
	case float64:
		numValue = float64(v)
		isNum = true
	case bool:
		C.envoy_dynamic_module_callback_http_set_dynamic_metadata_bool(
			h.hostPluginPtr,
			stringToModuleBuffer(metadataNamespace),
			stringToModuleBuffer(key),
			(C.bool)(v),
		)
		runtime.KeepAlive(metadataNamespace)
		runtime.KeepAlive(key)
		return
	case string:
		strValue = v
		isStr = true
	}

	if isNum {
		C.envoy_dynamic_module_callback_http_set_dynamic_metadata_number(
			h.hostPluginPtr,
			stringToModuleBuffer(metadataNamespace),
			stringToModuleBuffer(key),
			(C.double)(numValue),
		)
	} else if isStr {
		C.envoy_dynamic_module_callback_http_set_dynamic_metadata_string(
			h.hostPluginPtr,
			stringToModuleBuffer(metadataNamespace),
			stringToModuleBuffer(key),
			stringToModuleBuffer(strValue),
		)
	}
	runtime.KeepAlive(metadataNamespace)
	runtime.KeepAlive(key)
	runtime.KeepAlive(strValue)
}

func (h *dymHttpFilterHandle) GetAttributeNumber(
	attributeID shared.AttributeID,
) (float64, bool) {
	var value C.uint64_t = 0

	ret := C.envoy_dynamic_module_callback_http_filter_get_attribute_int(
		h.hostPluginPtr,
		(C.envoy_dynamic_module_type_attribute_id)(attributeID),
		&value,
	)
	if !bool(ret) {
		return 0, false
	}

	return float64(value), true
}

func (h *dymHttpFilterHandle) GetAttributeString(
	attributeID shared.AttributeID,
) (shared.UnsafeEnvoyBuffer, bool) {
	var valueView C.envoy_dynamic_module_type_envoy_buffer

	ret := C.envoy_dynamic_module_callback_http_filter_get_attribute_string(
		h.hostPluginPtr,
		(C.envoy_dynamic_module_type_attribute_id)(attributeID),
		&valueView,
	)
	if !bool(ret) || valueView.ptr == nil || valueView.length == 0 {
		return shared.UnsafeEnvoyBuffer{}, false
	}

	return envoyBufferToUnsafeEnvoyBuffer(valueView), true
}

func (h *dymHttpFilterHandle) GetAttributeBool(
	attributeID shared.AttributeID,
) (bool, bool) {
	var value C.bool

	ret := C.envoy_dynamic_module_callback_http_filter_get_attribute_bool(
		h.hostPluginPtr,
		(C.envoy_dynamic_module_type_attribute_id)(attributeID),
		&value,
	)
	if !bool(ret) {
		return false, false
	}

	return bool(value), true
}

func (h *dymHttpFilterHandle) GetFilterStateTyped(key string) (shared.UnsafeEnvoyBuffer, bool) {
	var valueView C.envoy_dynamic_module_type_envoy_buffer

	ret := C.envoy_dynamic_module_callback_http_get_filter_state_typed(
		h.hostPluginPtr,
		stringToModuleBuffer(key),
		&valueView,
	)
	runtime.KeepAlive(key)
	if !bool(ret) {
		return shared.UnsafeEnvoyBuffer{}, false
	}
	if valueView.ptr == nil || valueView.length == 0 {
		return shared.UnsafeEnvoyBuffer{}, true
	}
	return envoyBufferToUnsafeEnvoyBuffer(valueView), true
}

func (h *dymHttpFilterHandle) GetFilterState(key string) (shared.UnsafeEnvoyBuffer, bool) {
	var valueView C.envoy_dynamic_module_type_envoy_buffer

	ret := C.envoy_dynamic_module_callback_http_get_filter_state_bytes(
		h.hostPluginPtr,
		stringToModuleBuffer(key),
		&valueView,
	)
	if !bool(ret) || valueView.ptr == nil || valueView.length == 0 {
		return shared.UnsafeEnvoyBuffer{}, false
	}

	runtime.KeepAlive(key)
	return envoyBufferToUnsafeEnvoyBuffer(valueView), true
}

func (h *dymHttpFilterHandle) SetFilterState(key string, value []byte) {
	C.envoy_dynamic_module_callback_http_set_filter_state_bytes(
		h.hostPluginPtr,
		stringToModuleBuffer(key),
		bytesToModuleBuffer(value),
	)
	runtime.KeepAlive(key)
	runtime.KeepAlive(value)
}

func (h *dymHttpFilterHandle) SetFilterStateTyped(key string, value []byte) bool {
	ret := C.envoy_dynamic_module_callback_http_set_filter_state_typed(
		h.hostPluginPtr,
		stringToModuleBuffer(key),
		bytesToModuleBuffer(value),
	)
	runtime.KeepAlive(key)
	runtime.KeepAlive(value)
	return bool(ret)
}

func (h *dymHttpFilterHandle) GetData(key string) any {
	buf, found := h.GetMetadataString(shared.MetadataSourceTypeDynamic,
		"composer.shared_data", key)
	if !found {
		return nil
	}
	// Convert string back to uintptr safely.
	uintValue, err := strconv.ParseUint(buf.ToUnsafeString(), 10, 64)
	if err != nil {
		return nil
	}
	pointer := uintptr(uintValue)
	// Use search rather than unwrap because the go runtime will complain
	// the pointer parsed from string `pointer arithmetic result points to invalid allocation`.
	wrapper := sharedDataManager.search(pointer)
	if wrapper == nil {
		return nil
	}
	return wrapper.data
}

func (h *dymHttpFilterHandle) SetData(key string, value any) {
	wrapper := &httpFilterSharedDataWrapper{data: value}
	pointer := sharedDataManager.record(wrapper)
	h.recordedSharedData = append(h.recordedSharedData, pointer)

	// Covert pointer to uintptr to string safely.
	stringValue := strconv.FormatUint(uint64(uintptr(pointer)), 10)
	h.SetMetadata("composer.shared_data", key, stringValue)
}

func (h *dymHttpFilterHandle) clearData() {
	for _, pointer := range h.recordedSharedData {
		sharedDataManager.remove(pointer)
	}
}

func (h *dymHttpFilterHandle) SendLocalResponse(
	statusCode uint32,
	headers [][2]string,
	body []byte,
	detail string,
) {
	h.localResponseSent = true

	// Prepare headers.
	headerViews := headersToModuleHttpHeaderSlice(headers)
	C.envoy_dynamic_module_callback_http_send_response(
		h.hostPluginPtr,
		(C.uint32_t)(statusCode),
		unsafe.SliceData(headerViews),
		(C.size_t)(len(headerViews)),
		bytesToModuleBuffer(body),
		stringToModuleBuffer(detail),
	)

	runtime.KeepAlive(body)
	runtime.KeepAlive(detail)
	runtime.KeepAlive(headers)
}

func (h *dymHttpFilterHandle) SendResponseHeaders(
	headers [][2]string, endOfStream bool,
) {
	// Prepare headers.
	headerViews := headersToModuleHttpHeaderSlice(headers)
	C.envoy_dynamic_module_callback_http_send_response_headers(
		h.hostPluginPtr,
		unsafe.SliceData(headerViews),
		(C.size_t)(len(headerViews)),
		(C.bool)(endOfStream),
	)
	runtime.KeepAlive(headers)
	runtime.KeepAlive(headerViews)
}

func (h *dymHttpFilterHandle) SendResponseData(
	data []byte, endOfStream bool,
) {
	C.envoy_dynamic_module_callback_http_send_response_data(
		h.hostPluginPtr,
		bytesToModuleBuffer(data),
		(C.bool)(endOfStream),
	)
	runtime.KeepAlive(data)
}

func (h *dymHttpFilterHandle) SendResponseTrailers(
	trailers [][2]string,
) {
	// Prepare trailers.
	// Prepare trailers.
	trailerViews := headersToModuleHttpHeaderSlice(trailers)
	C.envoy_dynamic_module_callback_http_send_response_trailers(
		h.hostPluginPtr,
		unsafe.SliceData(trailerViews),
		(C.size_t)(len(trailerViews)),
	)
	runtime.KeepAlive(trailers)
	runtime.KeepAlive(trailerViews)
}

func (h *dymHttpFilterHandle) AddCustomFlag(flag string) {
	C.envoy_dynamic_module_callback_http_add_custom_flag(
		h.hostPluginPtr,
		stringToModuleBuffer(flag),
	)
}

func (h *dymHttpFilterHandle) ContinueRequest() {
	C.envoy_dynamic_module_callback_http_filter_continue_decoding(
		(C.envoy_dynamic_module_type_http_filter_envoy_ptr)(h.hostPluginPtr),
	)
}

func (h *dymHttpFilterHandle) ContinueResponse() {
	C.envoy_dynamic_module_callback_http_filter_continue_encoding(
		(C.envoy_dynamic_module_type_http_filter_envoy_ptr)(h.hostPluginPtr),
	)
}

func (h *dymHttpFilterHandle) ClearRouteCache() {
	C.envoy_dynamic_module_callback_http_clear_route_cache(h.hostPluginPtr)
}

func (h *dymHttpFilterHandle) RefreshRouteCluster() {
	C.envoy_dynamic_module_callback_http_clear_route_cluster_cache(h.hostPluginPtr)
}

func (h *dymHttpFilterHandle) GetWorkerIndex() uint32 {
	return uint32(C.envoy_dynamic_module_callback_http_filter_get_worker_index(h.hostPluginPtr))
}

func (h *dymHttpFilterHandle) SetSocketOptionInt(
	level, name int64,
	state shared.SocketOptionState,
	direction shared.SocketDirection,
	value int64,
) bool {
	ret := C.envoy_dynamic_module_callback_http_set_socket_option_int(
		h.hostPluginPtr,
		C.int64_t(level),
		C.int64_t(name),
		C.envoy_dynamic_module_type_socket_option_state(state),
		C.envoy_dynamic_module_type_socket_direction(direction),
		C.int64_t(value),
	)
	return bool(ret)
}

func (h *dymHttpFilterHandle) SetSocketOptionBytes(
	level, name int64,
	state shared.SocketOptionState,
	direction shared.SocketDirection,
	value []byte,
) bool {
	ret := C.envoy_dynamic_module_callback_http_set_socket_option_bytes(
		h.hostPluginPtr,
		C.int64_t(level),
		C.int64_t(name),
		C.envoy_dynamic_module_type_socket_option_state(state),
		C.envoy_dynamic_module_type_socket_direction(direction),
		bytesToModuleBuffer(value),
	)
	runtime.KeepAlive(value)
	return bool(ret)
}

func (h *dymHttpFilterHandle) GetSocketOptionInt(
	level, name int64,
	state shared.SocketOptionState,
	direction shared.SocketDirection,
) (int64, bool) {
	var value C.int64_t
	ret := C.envoy_dynamic_module_callback_http_get_socket_option_int(
		h.hostPluginPtr,
		C.int64_t(level),
		C.int64_t(name),
		C.envoy_dynamic_module_type_socket_option_state(state),
		C.envoy_dynamic_module_type_socket_direction(direction),
		&value,
	)
	if !bool(ret) {
		return 0, false
	}
	return int64(value), true
}

func (h *dymHttpFilterHandle) GetSocketOptionBytes(
	level, name int64,
	state shared.SocketOptionState,
	direction shared.SocketDirection,
) (shared.UnsafeEnvoyBuffer, bool) {
	var valueView C.envoy_dynamic_module_type_envoy_buffer
	ret := C.envoy_dynamic_module_callback_http_get_socket_option_bytes(
		h.hostPluginPtr,
		C.int64_t(level),
		C.int64_t(name),
		C.envoy_dynamic_module_type_socket_option_state(state),
		C.envoy_dynamic_module_type_socket_direction(direction),
		&valueView,
	)
	if !bool(ret) {
		return shared.UnsafeEnvoyBuffer{}, false
	}
	if valueView.ptr == nil || valueView.length == 0 {
		return shared.UnsafeEnvoyBuffer{}, true
	}
	return envoyBufferToUnsafeEnvoyBuffer(valueView), true
}

func (h *dymHttpFilterHandle) GetBufferLimit() uint64 {
	return uint64(C.envoy_dynamic_module_callback_http_get_buffer_limit(h.hostPluginPtr))
}

func (h *dymHttpFilterHandle) SetBufferLimit(limit uint64) {
	C.envoy_dynamic_module_callback_http_set_buffer_limit(h.hostPluginPtr, C.uint64_t(limit))
}

func (h *dymHttpFilterHandle) GetActiveSpan() shared.Span {
	spanPtr := C.envoy_dynamic_module_callback_http_get_active_span(h.hostPluginPtr)
	if spanPtr == nil {
		return nil
	}
	return &dymSpan{
		hostPluginPtr: h.hostPluginPtr,
		spanPtr:       spanPtr,
	}
}

func (h *dymHttpFilterHandle) GetClusterName() (shared.UnsafeEnvoyBuffer, bool) {
	var valueView C.envoy_dynamic_module_type_envoy_buffer
	ret := C.envoy_dynamic_module_callback_http_get_cluster_name(h.hostPluginPtr, &valueView)
	if !bool(ret) {
		return shared.UnsafeEnvoyBuffer{}, false
	}
	if valueView.ptr == nil || valueView.length == 0 {
		return shared.UnsafeEnvoyBuffer{}, true
	}
	return envoyBufferToUnsafeEnvoyBuffer(valueView), true
}

func (h *dymHttpFilterHandle) GetClusterHostCounts(priority uint32) (shared.ClusterHostCounts, bool) {
	var total C.size_t
	var healthy C.size_t
	var degraded C.size_t
	ret := C.envoy_dynamic_module_callback_http_get_cluster_host_count(
		h.hostPluginPtr,
		C.uint32_t(priority),
		&total,
		&healthy,
		&degraded,
	)
	if !bool(ret) {
		return shared.ClusterHostCounts{}, false
	}
	return shared.ClusterHostCounts{
		Total:    uint64(total),
		Healthy:  uint64(healthy),
		Degraded: uint64(degraded),
	}, true
}

func (h *dymHttpFilterHandle) SetUpstreamOverrideHost(host string, strict bool) bool {
	ret := C.envoy_dynamic_module_callback_http_set_upstream_override_host(
		h.hostPluginPtr,
		stringToModuleBuffer(host),
		C.bool(strict),
	)
	runtime.KeepAlive(host)
	return bool(ret)
}

func (h *dymHttpFilterHandle) ResetStream(reason shared.HttpFilterStreamResetReason, details string) {
	C.envoy_dynamic_module_callback_http_filter_reset_stream(
		h.hostPluginPtr,
		C.envoy_dynamic_module_type_http_filter_stream_reset_reason(reason),
		stringToModuleBuffer(details),
	)
	runtime.KeepAlive(details)
}

func (h *dymHttpFilterHandle) SendGoAwayAndClose(graceful bool) {
	C.envoy_dynamic_module_callback_http_filter_send_go_away_and_close(
		h.hostPluginPtr,
		C.bool(graceful),
	)
}

func (h *dymHttpFilterHandle) RecreateStream(headers [][2]string) bool {
	headerViews := headersToModuleHttpHeaderSlice(headers)
	ret := C.envoy_dynamic_module_callback_http_filter_recreate_stream(
		h.hostPluginPtr,
		unsafe.SliceData(headerViews),
		C.size_t(len(headerViews)),
	)
	runtime.KeepAlive(headers)
	runtime.KeepAlive(headerViews)
	return bool(ret)
}

func (h *dymHttpFilterHandle) RequestHeaders() shared.HeaderMap {
	return &h.requestHeaderMap
}

func (h *dymHttpFilterHandle) BufferedRequestBody() shared.BodyBuffer {
	return &h.bufferedRequestBody
}

func (h *dymHttpFilterHandle) ReceivedRequestBody() shared.BodyBuffer {
	return &h.receivedRequestBody
}

func (h *dymHttpFilterHandle) RequestTrailers() shared.HeaderMap {
	return &h.requestTrailerMap
}

func (h *dymHttpFilterHandle) ResponseHeaders() shared.HeaderMap {
	return &h.responseHeaderMap
}

func (h *dymHttpFilterHandle) BufferedResponseBody() shared.BodyBuffer {
	return &h.bufferedResponseBody
}

func (h *dymHttpFilterHandle) ReceivedResponseBody() shared.BodyBuffer {
	return &h.receivedResponseBody
}

func (h *dymHttpFilterHandle) ReceivedBufferedRequestBody() bool {
	return bool(C.envoy_dynamic_module_callback_http_received_buffered_request_body(
		h.hostPluginPtr,
	))
}

func (h *dymHttpFilterHandle) ReceivedBufferedResponseBody() bool {
	return bool(C.envoy_dynamic_module_callback_http_received_buffered_response_body(
		h.hostPluginPtr,
	))
}

func (h *dymHttpFilterHandle) ResponseTrailers() shared.HeaderMap {
	return &h.responseTrailerMap
}

func (h *dymHttpFilterHandle) GetMostSpecificConfig() any {
	perRoutePtr := C.envoy_dynamic_module_callback_get_most_specific_route_config(
		h.hostPluginPtr,
	)
	if perRoutePtr != nil {
		w := configPerRouteManager.unwrap(unsafe.Pointer(perRoutePtr))
		return w.config
	}
	return nil
}

func (h *dymHttpFilterHandle) GetScheduler() shared.Scheduler {
	if h.scheduler == nil {
		// The scheduler is created lazily and should never be nil
		// in practice. But it will be nil in mock tests.
		schedulerPtr := C.envoy_dynamic_module_callback_http_filter_scheduler_new(
			h.hostPluginPtr)
		h.scheduler = newDymScheduler(
			unsafe.Pointer(schedulerPtr),
			func(schedulerPtr unsafe.Pointer, taskID C.uint64_t) {
				C.envoy_dynamic_module_callback_http_filter_scheduler_commit(
					(C.envoy_dynamic_module_type_http_filter_scheduler_module_ptr)(schedulerPtr),
					taskID,
				)
			},
		)

		runtime.SetFinalizer(h.scheduler, func(s *dymScheduler) {
			C.envoy_dynamic_module_callback_http_filter_scheduler_delete(
				(C.envoy_dynamic_module_type_http_filter_scheduler_module_ptr)(s.schedulerPtr),
			)
		})
	}
	return h.scheduler
}

func (h *dymHttpFilterHandle) Log(level shared.LogLevel, format string, args ...any) {
	hostLog(level, format, args)
}

func (h *dymHttpFilterHandle) LogEnabled(level shared.LogLevel) bool {
	return bool(C.envoy_dynamic_module_callback_log_enabled(
		(C.envoy_dynamic_module_type_log_level)(uint32(level)),
	))
}

func (h *dymHttpFilterHandle) HttpCallout(
	cluster string, headers [][2]string, body []byte, timeoutMs uint64,
	cb shared.HttpCalloutCallback) (shared.HttpCalloutInitResult, uint64) {
	// Prepare headers.
	headerViews := headersToModuleHttpHeaderSlice(headers)
	var calloutID C.uint64_t = 0

	result := C.envoy_dynamic_module_callback_http_filter_http_callout(
		h.hostPluginPtr,
		&calloutID,
		stringToModuleBuffer(cluster),
		unsafe.SliceData(headerViews),
		(C.size_t)(len(headerViews)),
		bytesToModuleBuffer(body),
		(C.uint64_t)(timeoutMs),
	)

	runtime.KeepAlive(cluster)
	runtime.KeepAlive(headers)
	runtime.KeepAlive(body)
	runtime.KeepAlive(headerViews)

	goResult := shared.HttpCalloutInitResult(result)
	if goResult != shared.HttpCalloutInitSuccess {
		return goResult, 0
	}

	if h.calloutCallbacks == nil {
		h.calloutCallbacks = make(map[uint64]shared.HttpCalloutCallback)
	}
	h.calloutCallbacks[uint64(calloutID)] = cb

	return goResult, uint64(calloutID)
}

func (h *dymHttpFilterHandle) StartHttpStream(
	cluster string, headers [][2]string, body []byte, endOfStream bool, timeoutMs uint64,
	cb shared.HttpStreamCallback) (shared.HttpCalloutInitResult, uint64) {
	// Prepare headers.
	headerViews := headersToModuleHttpHeaderSlice(headers)
	var streamID C.uint64_t = 0

	result := C.envoy_dynamic_module_callback_http_filter_start_http_stream(
		h.hostPluginPtr,
		&streamID,
		stringToModuleBuffer(cluster),
		unsafe.SliceData(headerViews),
		(C.size_t)(len(headerViews)),
		bytesToModuleBuffer(body),
		(C.bool)(endOfStream),
		(C.uint64_t)(timeoutMs),
	)

	runtime.KeepAlive(cluster)
	runtime.KeepAlive(headers)
	runtime.KeepAlive(body)
	runtime.KeepAlive(headerViews)

	goResult := shared.HttpCalloutInitResult(result)
	if goResult != shared.HttpCalloutInitSuccess {
		return goResult, 0
	}

	if h.streamCallbacks == nil {
		h.streamCallbacks = make(map[uint64]shared.HttpStreamCallback)
	}
	h.streamCallbacks[uint64(streamID)] = cb

	return goResult, uint64(streamID)
}

func (h *dymHttpFilterHandle) SendHttpStreamData(
	streamID uint64, data []byte, endOfStream bool,
) bool {
	ret := C.envoy_dynamic_module_callback_http_stream_send_data(
		h.hostPluginPtr,
		(C.uint64_t)(streamID),
		bytesToModuleBuffer(data),
		(C.bool)(endOfStream),
	)
	runtime.KeepAlive(data)
	return bool(ret)
}

func (h *dymHttpFilterHandle) SendHttpStreamTrailers(
	streamID uint64, trailers [][2]string,
) bool {
	// Prepare trailers.
	trailerViews := headersToModuleHttpHeaderSlice(trailers)
	ret := C.envoy_dynamic_module_callback_http_stream_send_trailers(
		h.hostPluginPtr,
		(C.uint64_t)(streamID),
		unsafe.SliceData(trailerViews),
		(C.size_t)(len(trailerViews)),
	)
	runtime.KeepAlive(trailers)
	runtime.KeepAlive(trailerViews)
	return bool(ret)
}

func (h *dymHttpFilterHandle) ResetHttpStream(
	streamID uint64,
) {
	C.envoy_dynamic_module_callback_http_filter_reset_http_stream(
		h.hostPluginPtr,
		(C.uint64_t)(streamID),
	)
}

func (h *dymHttpFilterHandle) SetDownstreamWatermarkCallbacks(
	cbs shared.DownstreamWatermarkCallbacks,
) {
	h.downstreamWatermarkCallbacks = cbs
}

func (h *dymHttpFilterHandle) ClearDownstreamWatermarkCallbacks() {
	h.downstreamWatermarkCallbacks = nil
}

func (h *dymHttpFilterHandle) RecordHistogramValue(id shared.MetricID,
	value uint64, tagsValues ...string) shared.MetricsResult {
	idUint64 := uint64(id)
	// Prepare tag values.
	tagValueViews := stringArrayToModuleBufferSlice(tagsValues)

	ret := C.envoy_dynamic_module_callback_http_filter_record_histogram_value(
		h.hostPluginPtr,
		(C.size_t)(idUint64),
		unsafe.SliceData(tagValueViews),
		(C.size_t)(len(tagValueViews)),
		(C.uint64_t)(value),
	)

	runtime.KeepAlive(tagsValues)
	runtime.KeepAlive(tagValueViews)
	return shared.MetricsResult(ret)
}

func (h *dymHttpFilterHandle) SetGaugeValue(id shared.MetricID,
	value uint64, tagsValues ...string) shared.MetricsResult {
	idUint64 := uint64(id)
	// Prepare tag values.
	tagValueViews := stringArrayToModuleBufferSlice(tagsValues)

	ret := C.envoy_dynamic_module_callback_http_filter_set_gauge(
		h.hostPluginPtr,
		(C.size_t)(idUint64),
		unsafe.SliceData(tagValueViews),
		(C.size_t)(len(tagValueViews)),
		(C.uint64_t)(value),
	)

	runtime.KeepAlive(tagsValues)
	runtime.KeepAlive(tagValueViews)
	return shared.MetricsResult(ret)
}

func (h *dymHttpFilterHandle) IncrementGaugeValue(id shared.MetricID,
	value uint64, tagsValues ...string) shared.MetricsResult {
	// Prepare tag values.
	tagValueViews := stringArrayToModuleBufferSlice(tagsValues)
	ret := C.envoy_dynamic_module_callback_http_filter_increment_gauge(
		h.hostPluginPtr,
		(C.size_t)(uint64(id)),
		unsafe.SliceData(tagValueViews),
		(C.size_t)(len(tagValueViews)),
		(C.uint64_t)(value),
	)
	runtime.KeepAlive(tagsValues)
	runtime.KeepAlive(tagValueViews)
	return shared.MetricsResult(ret)
}

func (h *dymHttpFilterHandle) DecrementGaugeValue(id shared.MetricID,
	value uint64, tagsValues ...string) shared.MetricsResult {
	// Prepare tag values.
	tagValueViews := stringArrayToModuleBufferSlice(tagsValues)
	ret := C.envoy_dynamic_module_callback_http_filter_decrement_gauge(
		h.hostPluginPtr,
		(C.size_t)(uint64(id)),
		unsafe.SliceData(tagValueViews),
		(C.size_t)(len(tagValueViews)),
		(C.uint64_t)(value),
	)
	runtime.KeepAlive(tagsValues)
	runtime.KeepAlive(tagValueViews)
	return shared.MetricsResult(ret)
}

func (h *dymHttpFilterHandle) IncrementCounterValue(id shared.MetricID,
	value uint64, tagsValues ...string) shared.MetricsResult {
	// Prepare tag values.
	tagValueViews := stringArrayToModuleBufferSlice(tagsValues)
	ret := C.envoy_dynamic_module_callback_http_filter_increment_counter(
		h.hostPluginPtr,
		(C.size_t)(uint64(id)),
		unsafe.SliceData(tagValueViews),
		(C.size_t)(len(tagValueViews)),
		(C.uint64_t)(value),
	)
	runtime.KeepAlive(tagsValues)
	runtime.KeepAlive(tagValueViews)
	return shared.MetricsResult(ret)
}

// dymHttpFilterHandlePool is a pool of *dymHttpFilterHandle.
// Eliminates one heap allocation per request on the hot path.
// sync.Pool is non-deterministic; the GC may clear it at any cycle.
// That is intentional: we never need a handle to outlive a request.
var dymHttpFilterHandlePool = sync.Pool{
	New: func() any { return &dymHttpFilterHandle{} },
}

func newDymStreamPluginHandle(
	hostPluginPtr C.envoy_dynamic_module_type_http_filter_envoy_ptr,
) *dymHttpFilterHandle {
	h := dymHttpFilterHandlePool.Get().(*dymHttpFilterHandle)
	h.reset(hostPluginPtr)
	return h
}

// reset prepares a pooled handle for reuse with a new request.
// All per-request state is cleared. Backing arrays (recordedSharedData) are
// preserved to avoid reallocating on the next request.
func (h *dymHttpFilterHandle) reset(
	hostPluginPtr C.envoy_dynamic_module_type_http_filter_envoy_ptr,
) {
	h.hostPluginPtr = hostPluginPtr

	// Invariant checks: a handle coming off the pool must have nil plugin and
	// scheduler. The destroy callback zeros both before Put, so a non-nil value
	// here means something accessed the handle after it was returned, a
	// use-after-Put bug that would silently corrupt the next request's state.
	if h.plugin != nil {
		panic("BUG: handle pool corruption: plugin field still set when handle was reused from pool")
	}
	if h.scheduler != nil {
		panic("BUG: handle pool corruption: scheduler field still set when handle was reused from pool")
	}

	// Overwrite embedded value-type structs with the new hostPluginPtr.
	// Header/body type constants are fixed by the ABI.
	h.requestHeaderMap = dymHeaderMap{hostPluginPtr: hostPluginPtr, headerType: C.envoy_dynamic_module_type_http_header_type(0)}
	h.requestTrailerMap = dymHeaderMap{hostPluginPtr: hostPluginPtr, headerType: C.envoy_dynamic_module_type_http_header_type(1)}
	h.responseHeaderMap = dymHeaderMap{hostPluginPtr: hostPluginPtr, headerType: C.envoy_dynamic_module_type_http_header_type(2)}
	h.responseTrailerMap = dymHeaderMap{hostPluginPtr: hostPluginPtr, headerType: C.envoy_dynamic_module_type_http_header_type(3)}

	h.receivedRequestBody = dymBodyBuffer{hostPluginPtr: hostPluginPtr, bufferType: C.envoy_dynamic_module_type_http_body_type(0)}
	h.bufferedRequestBody = dymBodyBuffer{hostPluginPtr: hostPluginPtr, bufferType: C.envoy_dynamic_module_type_http_body_type(1)}
	h.receivedResponseBody = dymBodyBuffer{hostPluginPtr: hostPluginPtr, bufferType: C.envoy_dynamic_module_type_http_body_type(2)}
	h.bufferedResponseBody = dymBodyBuffer{hostPluginPtr: hostPluginPtr, bufferType: C.envoy_dynamic_module_type_http_body_type(3)}

	// Zero per-request state.
	h.plugin = nil
	h.scheduler = nil
	h.streamCompleted = false
	h.streamDestoried = false
	h.localResponseSent = false

	// nil maps, lazy-init on first callout/stream use.
	h.calloutCallbacks = nil
	h.streamCallbacks = nil

	// Reset slice length, preserve backing array capacity.
	h.recordedSharedData = h.recordedSharedData[:0]

	h.downstreamWatermarkCallbacks = nil
}

// dymConfigHandle is the per-filter-config handle. One instance exists for each
// filter config block in envoy.yaml (i.e., per listener/route, not per request).
// It is created in on_http_filter_config_new and destroyed in
// on_http_filter_config_destroy. It implements shared.HttpFilterConfigHandle.
type dymConfigHandle struct {
	hostConfigPtr    C.envoy_dynamic_module_type_http_filter_config_envoy_ptr
	calloutCallbacks map[uint64]shared.HttpCalloutCallback
	streamCallbacks  map[uint64]shared.HttpStreamCallback
	scheduler        *dymScheduler
}

func (h *dymConfigHandle) Log(level shared.LogLevel, format string, args ...any) {
	hostLog(level, format, args)
}

func (h *dymConfigHandle) DefineHistogram(name string,
	tagKeys ...string) (shared.MetricID, shared.MetricsResult) {
	// Prepare tag keys.
	tagKeyViews := stringArrayToModuleBufferSlice(tagKeys)

	var metricID C.size_t = 0

	var tagKeyPtr *C.envoy_dynamic_module_type_module_buffer = nil
	if len(tagKeyViews) > 0 {
		tagKeyPtr = unsafe.SliceData(tagKeyViews)
	}

	result := C.envoy_dynamic_module_callback_http_filter_config_define_histogram(
		h.hostConfigPtr,
		stringToModuleBuffer(name),
		tagKeyPtr,
		(C.size_t)(len(tagKeyViews)),
		&metricID,
	)

	runtime.KeepAlive(name)
	runtime.KeepAlive(tagKeys)
	runtime.KeepAlive(tagKeyViews)
	return shared.MetricID(metricID), shared.MetricsResult(result)
}

func (h *dymConfigHandle) DefineGauge(name string,
	tagKeys ...string) (shared.MetricID, shared.MetricsResult) {
	// Prepare tag keys.
	tagKeyViews := stringArrayToModuleBufferSlice(tagKeys)

	var metricID C.size_t = 0
	var tagKeyPtr *C.envoy_dynamic_module_type_module_buffer = nil
	if len(tagKeyViews) > 0 {
		tagKeyPtr = unsafe.SliceData(tagKeyViews)
	}

	result := C.envoy_dynamic_module_callback_http_filter_config_define_gauge(
		h.hostConfigPtr,
		stringToModuleBuffer(name),
		tagKeyPtr,
		(C.size_t)(len(tagKeyViews)),
		&metricID,
	)

	runtime.KeepAlive(name)
	runtime.KeepAlive(tagKeys)
	runtime.KeepAlive(tagKeyViews)
	return shared.MetricID(metricID), shared.MetricsResult(result)
}

func (h *dymConfigHandle) DefineCounter(name string,
	tagKeys ...string) (shared.MetricID, shared.MetricsResult) {
	// Prepare tag keys.
	tagKeyViews := stringArrayToModuleBufferSlice(tagKeys)

	var metricID C.size_t = 0
	var tagKeyPtr *C.envoy_dynamic_module_type_module_buffer = nil
	if len(tagKeyViews) > 0 {
		tagKeyPtr = unsafe.SliceData(tagKeyViews)
	}

	result := C.envoy_dynamic_module_callback_http_filter_config_define_counter(
		h.hostConfigPtr,
		stringToModuleBuffer(name),
		tagKeyPtr,
		(C.size_t)(len(tagKeyViews)),
		&metricID,
	)

	runtime.KeepAlive(name)
	runtime.KeepAlive(tagKeys)
	runtime.KeepAlive(tagKeyViews)
	return shared.MetricID(metricID), shared.MetricsResult(result)
}

func (h *dymConfigHandle) HttpCallout(
	cluster string, headers [][2]string, body []byte, timeoutMs uint64,
	cb shared.HttpCalloutCallback) (shared.HttpCalloutInitResult, uint64) {
	headerViews := headersToModuleHttpHeaderSlice(headers)
	var calloutID C.uint64_t = 0

	result := C.envoy_dynamic_module_callback_http_filter_config_http_callout(
		h.hostConfigPtr,
		&calloutID,
		stringToModuleBuffer(cluster),
		unsafe.SliceData(headerViews),
		(C.size_t)(len(headerViews)),
		bytesToModuleBuffer(body),
		(C.uint64_t)(timeoutMs),
	)

	runtime.KeepAlive(cluster)
	runtime.KeepAlive(headers)
	runtime.KeepAlive(body)
	runtime.KeepAlive(headerViews)

	goResult := shared.HttpCalloutInitResult(result)
	if goResult != shared.HttpCalloutInitSuccess {
		return goResult, 0
	}

	if h.calloutCallbacks == nil {
		h.calloutCallbacks = make(map[uint64]shared.HttpCalloutCallback)
	}
	h.calloutCallbacks[uint64(calloutID)] = cb

	return goResult, uint64(calloutID)
}

func (h *dymConfigHandle) StartHttpStream(
	cluster string, headers [][2]string, body []byte, endOfStream bool, timeoutMs uint64,
	cb shared.HttpStreamCallback) (shared.HttpCalloutInitResult, uint64) {
	headerViews := headersToModuleHttpHeaderSlice(headers)
	var streamID C.uint64_t = 0

	result := C.envoy_dynamic_module_callback_http_filter_config_start_http_stream(
		h.hostConfigPtr,
		&streamID,
		stringToModuleBuffer(cluster),
		unsafe.SliceData(headerViews),
		(C.size_t)(len(headerViews)),
		bytesToModuleBuffer(body),
		(C.bool)(endOfStream),
		(C.uint64_t)(timeoutMs),
	)

	runtime.KeepAlive(cluster)
	runtime.KeepAlive(headers)
	runtime.KeepAlive(body)
	runtime.KeepAlive(headerViews)

	goResult := shared.HttpCalloutInitResult(result)
	if goResult != shared.HttpCalloutInitSuccess {
		return goResult, 0
	}

	if h.streamCallbacks == nil {
		h.streamCallbacks = make(map[uint64]shared.HttpStreamCallback)
	}
	h.streamCallbacks[uint64(streamID)] = cb

	return goResult, uint64(streamID)
}

func (h *dymConfigHandle) SendHttpStreamData(streamID uint64, data []byte, endOfStream bool) bool {
	ret := C.envoy_dynamic_module_callback_http_filter_config_stream_send_data(
		h.hostConfigPtr,
		(C.uint64_t)(streamID),
		bytesToModuleBuffer(data),
		(C.bool)(endOfStream),
	)
	runtime.KeepAlive(data)
	return bool(ret)
}

func (h *dymConfigHandle) SendHttpStreamTrailers(streamID uint64, trailers [][2]string) bool {
	// Prepare trailers.
	trailerViews := headersToModuleHttpHeaderSlice(trailers)
	ret := C.envoy_dynamic_module_callback_http_filter_config_stream_send_trailers(
		h.hostConfigPtr,
		(C.uint64_t)(streamID),
		unsafe.SliceData(trailerViews),
		(C.size_t)(len(trailerViews)),
	)
	runtime.KeepAlive(trailers)
	runtime.KeepAlive(trailerViews)
	return bool(ret)
}

func (h *dymConfigHandle) ResetHttpStream(streamID uint64) {
	C.envoy_dynamic_module_callback_http_filter_config_reset_http_stream(
		h.hostConfigPtr,
		(C.uint64_t)(streamID),
	)
}

func (h *dymConfigHandle) GetScheduler() shared.Scheduler {
	if h.scheduler == nil {
		// The scheduler is created lazily and should never be nil
		// in practice. But it will be nil in mock tests.
		schedulerPtr := C.envoy_dynamic_module_callback_http_filter_config_scheduler_new(
			h.hostConfigPtr)
		h.scheduler = newDymScheduler(
			unsafe.Pointer(schedulerPtr),
			func(schedulerPtr unsafe.Pointer, taskID C.uint64_t) {
				C.envoy_dynamic_module_callback_http_filter_config_scheduler_commit(
					(C.envoy_dynamic_module_type_http_filter_config_scheduler_module_ptr)(schedulerPtr),
					taskID,
				)
			},
		)

		runtime.SetFinalizer(h.scheduler, func(s *dymScheduler) {
			C.envoy_dynamic_module_callback_http_filter_config_scheduler_delete(
				(C.envoy_dynamic_module_type_http_filter_config_scheduler_module_ptr)(s.schedulerPtr),
			)
		})
	}
	return h.scheduler
}

type dymRouteConfigHandle struct{}

func (h *dymRouteConfigHandle) Log(level shared.LogLevel, format string, args ...any) {
	hostLog(level, format, args)
}

func (h *dymRouteConfigHandle) DefineHistogram(name string,
	tagKeys ...string) (shared.MetricID, shared.MetricsResult) {
	return 0, shared.MetricsFrozen
}

func (h *dymRouteConfigHandle) DefineGauge(name string,
	tagKeys ...string) (shared.MetricID, shared.MetricsResult) {
	return 0, shared.MetricsFrozen
}

func (h *dymRouteConfigHandle) DefineCounter(name string,
	tagKeys ...string) (shared.MetricID, shared.MetricsResult) {
	return 0, shared.MetricsFrozen
}

// ABI callbacks: functions exported to C that Envoy calls at specific points
// in the filter lifecycle. Names match the ABI contract defined in abi/abi.h.
// These are the only functions with C linkage in the package.

//export envoy_dynamic_module_on_program_init
func envoy_dynamic_module_on_program_init() C.envoy_dynamic_module_type_abi_version_module_ptr {
	return C.envoy_dynamic_module_type_abi_version_module_ptr(C.envoy_dynamic_modules_abi_version)
}

//export envoy_dynamic_module_on_http_filter_config_new
func envoy_dynamic_module_on_http_filter_config_new(
	hostConfigPtr C.envoy_dynamic_module_type_http_filter_config_envoy_ptr,
	name C.envoy_dynamic_module_type_envoy_buffer,
	config C.envoy_dynamic_module_type_envoy_buffer,
) C.envoy_dynamic_module_type_http_filter_config_module_ptr {
	nameString := envoyBufferToStringUnsafe(name)
	configBytes := envoyBufferToBytesUnsafe(config)

	configHandle := &dymConfigHandle{hostConfigPtr: hostConfigPtr}
	factory, err := sdk.NewHttpFilterFactory(configHandle, nameString, configBytes)
	if err != nil || factory == nil {
		configHandle.Log(shared.LogLevelWarn, "Failed to load configuration: %v", err)
		return nil
	}
	configPtr := configManager.record(&httpFilterConfigWrapper{pluginFactory: factory, configHandle: configHandle})
	return C.envoy_dynamic_module_type_http_filter_config_module_ptr(configPtr)
}

//export envoy_dynamic_module_on_http_filter_config_destroy
func envoy_dynamic_module_on_http_filter_config_destroy(
	configPtr C.envoy_dynamic_module_type_http_filter_config_module_ptr,
) {
	factoryWrapper := configManager.unwrap(unsafe.Pointer(configPtr))
	if factoryWrapper == nil {
		return
	}
	factoryWrapper.configHandle.scheduler = nil
	factoryWrapper.pluginFactory.OnDestroy()
	configManager.remove(unsafe.Pointer(configPtr))
}

//export envoy_dynamic_module_on_http_filter_per_route_config_new
func envoy_dynamic_module_on_http_filter_per_route_config_new(
	name C.envoy_dynamic_module_type_envoy_buffer,
	config C.envoy_dynamic_module_type_envoy_buffer,
) C.envoy_dynamic_module_type_http_filter_per_route_config_module_ptr {
	nameStr := envoyBufferToStringUnsafe(name)
	configBytes := envoyBufferToBytesUnsafe(config)

	// The route config handle only make logging available.
	configHandle := &dymRouteConfigHandle{}

	configFactory := sdk.GetHttpFilterConfigFactory(nameStr)
	if configFactory == nil {
		configHandle.Log(shared.LogLevelWarn,
			"Failed to load configuration: no factory for %s", nameStr)
		return nil
	}
	parsedConfig, err := configFactory.CreatePerRoute(configBytes)
	if err != nil || parsedConfig == nil {
		configHandle.Log(shared.LogLevelWarn,
			"Failed to load per-route configuration: %v", err)
		return nil
	}

	configPtr := configPerRouteManager.record(&httpFilterConfigWrapperPerRoute{config: parsedConfig})
	return C.envoy_dynamic_module_type_http_filter_per_route_config_module_ptr(configPtr)
}

//export envoy_dynamic_module_on_http_filter_per_route_config_destroy
func envoy_dynamic_module_on_http_filter_per_route_config_destroy(
	configPtr C.envoy_dynamic_module_type_http_filter_per_route_config_module_ptr,
) {
	configPerRouteManager.remove(unsafe.Pointer(configPtr))
}

//export envoy_dynamic_module_on_http_filter_new
func envoy_dynamic_module_on_http_filter_new(
	pluginConfigPtr C.envoy_dynamic_module_type_http_filter_config_module_ptr,
	hostPluginPtr C.envoy_dynamic_module_type_http_filter_envoy_ptr,
) C.envoy_dynamic_module_type_http_filter_module_ptr {
	factoryWrapper := configManager.unwrap(unsafe.Pointer(pluginConfigPtr))
	if factoryWrapper == nil {
		return nil
	}

	// Create the plugin wrapper.

	pluginWrapper := newDymStreamPluginHandle(hostPluginPtr)
	pluginWrapper.plugin = factoryWrapper.pluginFactory.Create(pluginWrapper)
	pluginPtr := pluginManager.record(pluginWrapper)
	return C.envoy_dynamic_module_type_http_filter_module_ptr(pluginPtr)
}

//export envoy_dynamic_module_on_http_filter_destroy
func envoy_dynamic_module_on_http_filter_destroy(
	pluginPtr C.envoy_dynamic_module_type_http_filter_module_ptr,
) {
	pluginWrapper := pluginManager.unwrap(unsafe.Pointer(pluginPtr))
	if pluginWrapper == nil || pluginWrapper.streamDestoried {
		return
	}
	pluginWrapper.streamDestoried = true
	if pluginWrapper.plugin != nil {
		pluginWrapper.plugin.OnDestroy()
	}
	pluginManager.remove(unsafe.Pointer(pluginPtr))
	// destroy is the last callback Envoy calls for a filter instance.
	// It is safe to return the handle to the pool here regardless of whether
	// stream_complete already ran. Zero plugin before Put so the pool assertion
	// in reset() does not fire on the next Get.
	pluginWrapper.plugin = nil
	pluginWrapper.scheduler = nil
	dymHttpFilterHandlePool.Put(pluginWrapper)
}

//export envoy_dynamic_module_on_http_filter_request_headers
func envoy_dynamic_module_on_http_filter_request_headers(
	_ C.envoy_dynamic_module_type_http_filter_envoy_ptr,
	pluginPtr C.envoy_dynamic_module_type_http_filter_module_ptr,
	endOfStream C.bool,
) C.envoy_dynamic_module_type_on_http_filter_request_headers_status {
	// Get the plugin wrapper.
	pluginWrapper := pluginManager.unwrap(unsafe.Pointer(pluginPtr))
	if pluginWrapper == nil || pluginWrapper.plugin == nil {
		return 0
	}

	return C.envoy_dynamic_module_type_on_http_filter_request_headers_status(
		pluginWrapper.plugin.OnRequestHeaders(&pluginWrapper.requestHeaderMap, bool(endOfStream)))
}

//export envoy_dynamic_module_on_http_filter_request_body
func envoy_dynamic_module_on_http_filter_request_body(
	_ C.envoy_dynamic_module_type_http_filter_envoy_ptr,
	pluginPtr C.envoy_dynamic_module_type_http_filter_module_ptr,
	endOfStream C.bool,
) C.envoy_dynamic_module_type_on_http_filter_request_body_status {
	pluginWrapper := pluginManager.unwrap(unsafe.Pointer(pluginPtr))
	if pluginWrapper == nil || pluginWrapper.plugin == nil {
		return 0
	}
	return C.envoy_dynamic_module_type_on_http_filter_request_body_status(
		pluginWrapper.plugin.OnRequestBody(&pluginWrapper.receivedRequestBody, bool(endOfStream)))
}

//export envoy_dynamic_module_on_http_filter_request_trailers
func envoy_dynamic_module_on_http_filter_request_trailers(
	_ C.envoy_dynamic_module_type_http_filter_envoy_ptr,
	pluginPtr C.envoy_dynamic_module_type_http_filter_module_ptr,
) C.envoy_dynamic_module_type_on_http_filter_request_trailers_status {
	pluginWrapper := pluginManager.unwrap(unsafe.Pointer(pluginPtr))
	if pluginWrapper == nil || pluginWrapper.plugin == nil {
		return 0
	}
	return C.envoy_dynamic_module_type_on_http_filter_request_trailers_status(
		pluginWrapper.plugin.OnRequestTrailers(&pluginWrapper.requestTrailerMap))
}

//export envoy_dynamic_module_on_http_filter_response_headers
func envoy_dynamic_module_on_http_filter_response_headers(
	_ C.envoy_dynamic_module_type_http_filter_envoy_ptr,
	pluginPtr C.envoy_dynamic_module_type_http_filter_module_ptr,
	endOfStream C.bool,
) C.envoy_dynamic_module_type_on_http_filter_response_headers_status {
	pluginWrapper := pluginManager.unwrap(unsafe.Pointer(pluginPtr))
	if pluginWrapper == nil || pluginWrapper.plugin == nil || pluginWrapper.localResponseSent {
		return 0
	}
	return C.envoy_dynamic_module_type_on_http_filter_response_headers_status(
		pluginWrapper.plugin.OnResponseHeaders(&pluginWrapper.responseHeaderMap, bool(endOfStream)))
}

//export envoy_dynamic_module_on_http_filter_response_body
func envoy_dynamic_module_on_http_filter_response_body(
	_ C.envoy_dynamic_module_type_http_filter_envoy_ptr,
	pluginPtr C.envoy_dynamic_module_type_http_filter_module_ptr,
	endOfStream C.bool,
) C.envoy_dynamic_module_type_on_http_filter_response_body_status {
	pluginWrapper := pluginManager.unwrap(unsafe.Pointer(pluginPtr))
	if pluginWrapper == nil || pluginWrapper.plugin == nil || pluginWrapper.localResponseSent {
		return 0
	}
	return C.envoy_dynamic_module_type_on_http_filter_response_body_status(
		pluginWrapper.plugin.OnResponseBody(&pluginWrapper.receivedResponseBody, bool(endOfStream)))
}

//export envoy_dynamic_module_on_http_filter_response_trailers
func envoy_dynamic_module_on_http_filter_response_trailers(
	_ C.envoy_dynamic_module_type_http_filter_envoy_ptr,
	pluginPtr C.envoy_dynamic_module_type_http_filter_module_ptr,
) C.envoy_dynamic_module_type_on_http_filter_response_trailers_status {
	pluginWrapper := pluginManager.unwrap(unsafe.Pointer(pluginPtr))
	if pluginWrapper == nil || pluginWrapper.plugin == nil || pluginWrapper.localResponseSent {
		return 0
	}
	return C.envoy_dynamic_module_type_on_http_filter_response_trailers_status(
		pluginWrapper.plugin.OnResponseTrailers(&pluginWrapper.responseTrailerMap))
}

//export envoy_dynamic_module_on_http_filter_stream_complete
func envoy_dynamic_module_on_http_filter_stream_complete(
	_ C.envoy_dynamic_module_type_http_filter_envoy_ptr,
	pluginPtr C.envoy_dynamic_module_type_http_filter_module_ptr,
) {
	pluginWrapper := pluginManager.unwrap(unsafe.Pointer(pluginPtr))
	if pluginWrapper == nil || pluginWrapper.plugin == nil {
		return
	}
	pluginWrapper.streamCompleted = true
	pluginWrapper.clearData()
	pluginWrapper.plugin.OnStreamComplete()
	// Do NOT return to pool here. Envoy may still call on_http_filter_destroy
	// after stream_complete on some code paths. The destroy callback is the
	// single safe point for pool return since it is the last callback Envoy
	// will ever make for this filter instance.
}

//export envoy_dynamic_module_on_http_filter_scheduled
func envoy_dynamic_module_on_http_filter_scheduled(
	_ C.envoy_dynamic_module_type_http_filter_envoy_ptr,
	pluginPtr C.envoy_dynamic_module_type_http_filter_module_ptr,
	taskID C.uint64_t,
) {
	pluginWrapper := pluginManager.unwrap(unsafe.Pointer(pluginPtr))
	if pluginWrapper == nil || pluginWrapper.scheduler == nil || pluginWrapper.streamCompleted {
		return
	}
	pluginWrapper.scheduler.onScheduled(uint64(taskID))
}

//export envoy_dynamic_module_on_http_filter_http_callout_done
func envoy_dynamic_module_on_http_filter_http_callout_done(
	_ C.envoy_dynamic_module_type_http_filter_envoy_ptr,
	pluginPtr C.envoy_dynamic_module_type_http_filter_module_ptr,
	calloutID C.uint64_t,
	result C.envoy_dynamic_module_type_http_callout_result,
	headers *C.envoy_dynamic_module_type_envoy_http_header,
	headersSize C.size_t,
	chunks *C.envoy_dynamic_module_type_envoy_buffer,
	chunksSize C.size_t,
) {
	pluginWrapper := pluginManager.unwrap(unsafe.Pointer(pluginPtr))
	if pluginWrapper == nil || pluginWrapper.streamCompleted {
		return
	}

	// Prepare headers and body chunks.
	resultHeaders := envoyHttpHeaderSliceToUnsafeHeaderSlice(unsafe.Slice(headers, int(headersSize)))
	resultChunks := envoyBufferSliceToUnsafeEnvoyBufferSlice(unsafe.Slice(chunks, int(chunksSize)))

	cb := pluginWrapper.calloutCallbacks[uint64(calloutID)]
	if cb != nil {
		delete(pluginWrapper.calloutCallbacks, uint64(calloutID))
		cb.OnHttpCalloutDone(uint64(calloutID),
			shared.HttpCalloutResult(result),
			resultHeaders,
			resultChunks,
		)
	}
}

//export envoy_dynamic_module_on_http_filter_http_stream_headers
func envoy_dynamic_module_on_http_filter_http_stream_headers(
	_ C.envoy_dynamic_module_type_http_filter_envoy_ptr,
	pluginPtr C.envoy_dynamic_module_type_http_filter_module_ptr,
	streamID C.uint64_t,
	headers *C.envoy_dynamic_module_type_envoy_http_header,
	headersSize C.size_t,
	endOfStream C.bool,
) {
	pluginWrapper := pluginManager.unwrap(unsafe.Pointer(pluginPtr))
	if pluginWrapper == nil || pluginWrapper.streamCompleted {
		return
	}

	// Prepare headers.
	resultHeaders := envoyHttpHeaderSliceToUnsafeHeaderSlice(unsafe.Slice(headers, int(headersSize)))

	cb := pluginWrapper.streamCallbacks[uint64(streamID)]
	if cb != nil {
		cb.OnHttpStreamHeaders(uint64(streamID), resultHeaders, bool(endOfStream))
	}
}

//export envoy_dynamic_module_on_http_filter_http_stream_data
func envoy_dynamic_module_on_http_filter_http_stream_data(
	_ C.envoy_dynamic_module_type_http_filter_envoy_ptr,
	pluginPtr C.envoy_dynamic_module_type_http_filter_module_ptr,
	streamID C.uint64_t,
	chunks C.ConstEnvoyBufferPtr,
	chunksSize C.size_t,
	endOfStream C.bool,
) {
	pluginWrapper := pluginManager.unwrap(unsafe.Pointer(pluginPtr))
	if pluginWrapper == nil || pluginWrapper.streamCompleted {
		return
	}

	// Prepare data.
	resultData := envoyBufferSliceToUnsafeEnvoyBufferSlice(unsafe.Slice(chunks, int(chunksSize)))

	cb := pluginWrapper.streamCallbacks[uint64(streamID)]
	if cb != nil {
		cb.OnHttpStreamData(uint64(streamID), resultData, bool(endOfStream))
	}
}

//export envoy_dynamic_module_on_http_filter_http_stream_trailers
func envoy_dynamic_module_on_http_filter_http_stream_trailers(
	_ C.envoy_dynamic_module_type_http_filter_envoy_ptr,
	pluginPtr C.envoy_dynamic_module_type_http_filter_module_ptr,
	streamID C.uint64_t,
	trailers *C.envoy_dynamic_module_type_envoy_http_header,
	trailersSize C.size_t,
) {
	pluginWrapper := pluginManager.unwrap(unsafe.Pointer(pluginPtr))
	if pluginWrapper == nil || pluginWrapper.streamCompleted {
		return
	}

	// Prepare trailers.
	resultTrailers := envoyHttpHeaderSliceToUnsafeHeaderSlice(unsafe.Slice(trailers, int(trailersSize)))

	cb := pluginWrapper.streamCallbacks[uint64(streamID)]
	if cb != nil {
		cb.OnHttpStreamTrailers(uint64(streamID), resultTrailers)
	}
}

//export envoy_dynamic_module_on_http_filter_http_stream_complete
func envoy_dynamic_module_on_http_filter_http_stream_complete(
	_ C.envoy_dynamic_module_type_http_filter_envoy_ptr,
	pluginPtr C.envoy_dynamic_module_type_http_filter_module_ptr,
	streamID C.uint64_t,
) {
	pluginWrapper := pluginManager.unwrap(unsafe.Pointer(pluginPtr))
	if pluginWrapper == nil || pluginWrapper.streamCompleted {
		return
	}

	cb := pluginWrapper.streamCallbacks[uint64(streamID)]
	if cb != nil {
		delete(pluginWrapper.streamCallbacks, uint64(streamID))
		cb.OnHttpStreamComplete(uint64(streamID))
	}
}

//export envoy_dynamic_module_on_http_filter_http_stream_reset
func envoy_dynamic_module_on_http_filter_http_stream_reset(
	_ C.envoy_dynamic_module_type_http_filter_envoy_ptr,
	pluginPtr C.envoy_dynamic_module_type_http_filter_module_ptr,
	streamID C.uint64_t,
	reason C.envoy_dynamic_module_type_http_stream_reset_reason,
) {
	pluginWrapper := pluginManager.unwrap(unsafe.Pointer(pluginPtr))
	if pluginWrapper == nil || pluginWrapper.streamCompleted {
		return
	}

	cb := pluginWrapper.streamCallbacks[uint64(streamID)]
	if cb != nil {
		delete(pluginWrapper.streamCallbacks, uint64(streamID))
		cb.OnHttpStreamReset(uint64(streamID), shared.HttpStreamResetReason(reason))
	}
}

//export envoy_dynamic_module_on_http_filter_downstream_above_write_buffer_high_watermark
func envoy_dynamic_module_on_http_filter_downstream_above_write_buffer_high_watermark(
	_ C.envoy_dynamic_module_type_http_filter_envoy_ptr,
	pluginPtr C.envoy_dynamic_module_type_http_filter_module_ptr,
) {
	pluginWrapper := pluginManager.unwrap(unsafe.Pointer(pluginPtr))
	if pluginWrapper == nil || pluginWrapper.streamCompleted {
		return
	}

	if pluginWrapper.downstreamWatermarkCallbacks != nil {
		pluginWrapper.downstreamWatermarkCallbacks.OnAboveWriteBufferHighWatermark()
	}
}

//export envoy_dynamic_module_on_http_filter_downstream_below_write_buffer_low_watermark
func envoy_dynamic_module_on_http_filter_downstream_below_write_buffer_low_watermark(
	_ C.envoy_dynamic_module_type_http_filter_envoy_ptr,
	pluginPtr C.envoy_dynamic_module_type_http_filter_module_ptr,
) {
	pluginWrapper := pluginManager.unwrap(unsafe.Pointer(pluginPtr))
	if pluginWrapper == nil || pluginWrapper.streamCompleted {
		return
	}

	if pluginWrapper.downstreamWatermarkCallbacks != nil {
		pluginWrapper.downstreamWatermarkCallbacks.OnBelowWriteBufferLowWatermark()
	}
}

//export envoy_dynamic_module_on_http_filter_local_reply
func envoy_dynamic_module_on_http_filter_local_reply(
	filter_envoy_ptr C.envoy_dynamic_module_type_http_filter_envoy_ptr,
	filter_module_ptr C.envoy_dynamic_module_type_http_filter_module_ptr,
	response_code C.uint32_t,
	details C.envoy_dynamic_module_type_envoy_buffer,
	reset_imminent C.bool,
) C.envoy_dynamic_module_type_on_http_filter_local_reply_status {
	_ = filter_envoy_ptr
	pluginWrapper := pluginManager.unwrap(unsafe.Pointer(filter_module_ptr))
	if pluginWrapper == nil || pluginWrapper.plugin == nil {
		return C.envoy_dynamic_module_type_on_http_filter_local_reply_status(
			shared.LocalReplyStatusContinue,
		)
	}

	return C.envoy_dynamic_module_type_on_http_filter_local_reply_status(
		pluginWrapper.plugin.OnLocalReply(
			uint32(response_code),
			envoyBufferToUnsafeEnvoyBuffer(details),
			bool(reset_imminent),
		),
	)
}

//export envoy_dynamic_module_on_http_filter_config_http_callout_done
func envoy_dynamic_module_on_http_filter_config_http_callout_done(
	_ C.envoy_dynamic_module_type_http_filter_config_envoy_ptr,
	configPtr C.envoy_dynamic_module_type_http_filter_config_module_ptr,
	calloutID C.uint64_t,
	result C.envoy_dynamic_module_type_http_callout_result,
	headers *C.envoy_dynamic_module_type_envoy_http_header,
	headersSize C.size_t,
	chunks *C.envoy_dynamic_module_type_envoy_buffer,
	chunksSize C.size_t,
) {
	configWrapper := configManager.unwrap(unsafe.Pointer(configPtr))
	if configWrapper == nil || configWrapper.configHandle == nil {
		return
	}
	ch := configWrapper.configHandle

	resultHeaders := envoyHttpHeaderSliceToUnsafeHeaderSlice(unsafe.Slice(headers, int(headersSize)))
	resultChunks := envoyBufferSliceToUnsafeEnvoyBufferSlice(unsafe.Slice(chunks, int(chunksSize)))

	cb := ch.calloutCallbacks[uint64(calloutID)]
	if cb != nil {
		delete(ch.calloutCallbacks, uint64(calloutID))
		cb.OnHttpCalloutDone(uint64(calloutID), shared.HttpCalloutResult(result), resultHeaders, resultChunks)
	}
}

//export envoy_dynamic_module_on_http_filter_config_http_stream_headers
func envoy_dynamic_module_on_http_filter_config_http_stream_headers(
	_ C.envoy_dynamic_module_type_http_filter_config_envoy_ptr,
	configPtr C.envoy_dynamic_module_type_http_filter_config_module_ptr,
	streamID C.uint64_t,
	headers *C.envoy_dynamic_module_type_envoy_http_header,
	headersSize C.size_t,
	endOfStream C.bool,
) {
	configWrapper := configManager.unwrap(unsafe.Pointer(configPtr))
	if configWrapper == nil || configWrapper.configHandle == nil {
		return
	}
	ch := configWrapper.configHandle

	resultHeaders := envoyHttpHeaderSliceToUnsafeHeaderSlice(unsafe.Slice(headers, int(headersSize)))

	cb := ch.streamCallbacks[uint64(streamID)]
	if cb != nil {
		cb.OnHttpStreamHeaders(uint64(streamID), resultHeaders, bool(endOfStream))
	}
}

//export envoy_dynamic_module_on_http_filter_config_http_stream_data
func envoy_dynamic_module_on_http_filter_config_http_stream_data(
	_ C.envoy_dynamic_module_type_http_filter_config_envoy_ptr,
	configPtr C.envoy_dynamic_module_type_http_filter_config_module_ptr,
	streamID C.uint64_t,
	chunks C.ConstEnvoyBufferPtr,
	chunksSize C.size_t,
	endOfStream C.bool,
) {
	configWrapper := configManager.unwrap(unsafe.Pointer(configPtr))
	if configWrapper == nil || configWrapper.configHandle == nil {
		return
	}
	ch := configWrapper.configHandle

	resultData := envoyBufferSliceToUnsafeEnvoyBufferSlice(unsafe.Slice(chunks, int(chunksSize)))

	cb := ch.streamCallbacks[uint64(streamID)]
	if cb != nil {
		cb.OnHttpStreamData(uint64(streamID), resultData, bool(endOfStream))
	}
}

//export envoy_dynamic_module_on_http_filter_config_http_stream_trailers
func envoy_dynamic_module_on_http_filter_config_http_stream_trailers(
	_ C.envoy_dynamic_module_type_http_filter_config_envoy_ptr,
	configPtr C.envoy_dynamic_module_type_http_filter_config_module_ptr,
	streamID C.uint64_t,
	trailers *C.envoy_dynamic_module_type_envoy_http_header,
	trailersSize C.size_t,
) {
	configWrapper := configManager.unwrap(unsafe.Pointer(configPtr))
	if configWrapper == nil || configWrapper.configHandle == nil {
		return
	}
	ch := configWrapper.configHandle

	resultTrailers := envoyHttpHeaderSliceToUnsafeHeaderSlice(unsafe.Slice(trailers, int(trailersSize)))

	cb := ch.streamCallbacks[uint64(streamID)]
	if cb != nil {
		cb.OnHttpStreamTrailers(uint64(streamID), resultTrailers)
	}
}

//export envoy_dynamic_module_on_http_filter_config_http_stream_complete
func envoy_dynamic_module_on_http_filter_config_http_stream_complete(
	_ C.envoy_dynamic_module_type_http_filter_config_envoy_ptr,
	configPtr C.envoy_dynamic_module_type_http_filter_config_module_ptr,
	streamID C.uint64_t,
) {
	configWrapper := configManager.unwrap(unsafe.Pointer(configPtr))
	if configWrapper == nil || configWrapper.configHandle == nil {
		return
	}
	ch := configWrapper.configHandle

	cb := ch.streamCallbacks[uint64(streamID)]
	if cb != nil {
		delete(ch.streamCallbacks, uint64(streamID))
		cb.OnHttpStreamComplete(uint64(streamID))
	}
}

//export envoy_dynamic_module_on_http_filter_config_http_stream_reset
func envoy_dynamic_module_on_http_filter_config_http_stream_reset(
	_ C.envoy_dynamic_module_type_http_filter_config_envoy_ptr,
	configPtr C.envoy_dynamic_module_type_http_filter_config_module_ptr,
	streamID C.uint64_t,
	reason C.envoy_dynamic_module_type_http_stream_reset_reason,
) {
	configWrapper := configManager.unwrap(unsafe.Pointer(configPtr))
	if configWrapper == nil || configWrapper.configHandle == nil {
		return
	}
	ch := configWrapper.configHandle

	cb := ch.streamCallbacks[uint64(streamID)]
	if cb != nil {
		delete(ch.streamCallbacks, uint64(streamID))
		cb.OnHttpStreamReset(uint64(streamID), shared.HttpStreamResetReason(reason))
	}
}

//export envoy_dynamic_module_on_http_filter_config_scheduled
func envoy_dynamic_module_on_http_filter_config_scheduled(
	_ C.envoy_dynamic_module_type_http_filter_config_envoy_ptr,
	configPtr C.envoy_dynamic_module_type_http_filter_config_module_ptr,
	taskID C.uint64_t,
) {
	configWrapper := configManager.unwrap(unsafe.Pointer(configPtr))
	if configWrapper == nil || configWrapper.configHandle == nil {
		return
	}
	ch := configWrapper.configHandle

	if ch.scheduler != nil {
		ch.scheduler.onScheduled(uint64(taskID))
	}
}
