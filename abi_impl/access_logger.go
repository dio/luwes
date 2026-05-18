package abi_impl

/*
#include "../abi/abi.h"
*/
import "C"
import (
	"runtime"
	"unsafe"

	sdk "github.com/dio/luwes"
	"github.com/dio/luwes/shared"
)

// accessLoggerConfigWrapper holds the per-config factory and the Envoy config pointer.
// Created on the main thread in envoy_dynamic_module_on_access_logger_config_new.
type accessLoggerConfigWrapper struct {
	factory      shared.AccessLoggerFactory
	configHandle *dymAccessLoggerConfigHandle
}

// accessLoggerWrapper holds one per-worker AccessLogger instance.
// Created per worker thread in envoy_dynamic_module_on_access_logger_new.
type accessLoggerWrapper struct {
	logger    shared.AccessLogger
	configPtr unsafe.Pointer // back-pointer to config wrapper for stats callbacks
}

var (
	accessLoggerConfigManager = newManager[accessLoggerConfigWrapper]()
	accessLoggerManager       = newManager[accessLoggerWrapper]()
)

// dymAccessLoggerConfigHandle wraps an access logger config Envoy pointer.
// Implements shared.AccessLoggerConfigHandle. Used on the main thread only.
type dymAccessLoggerConfigHandle struct {
	configPtr C.envoy_dynamic_module_type_access_logger_config_envoy_ptr
}

func (h *dymAccessLoggerConfigHandle) Log(level shared.LogLevel, format string, args ...any) {
	hostLog(level, format, args)
}

func (h *dymAccessLoggerConfigHandle) DefineCounter(name string,
	tagKeys ...string) (shared.MetricID, shared.MetricsResult) {
	var metricID C.size_t
	result := C.envoy_dynamic_module_callback_access_logger_config_define_counter(
		h.configPtr,
		stringToModuleBuffer(name),
		&metricID,
	)
	runtime.KeepAlive(name)
	return shared.MetricID(metricID), shared.MetricsResult(result)
}

func (h *dymAccessLoggerConfigHandle) DefineGauge(name string,
	tagKeys ...string) (shared.MetricID, shared.MetricsResult) {
	var metricID C.size_t
	result := C.envoy_dynamic_module_callback_access_logger_config_define_gauge(
		h.configPtr,
		stringToModuleBuffer(name),
		&metricID,
	)
	runtime.KeepAlive(name)
	return shared.MetricID(metricID), shared.MetricsResult(result)
}

func (h *dymAccessLoggerConfigHandle) DefineHistogram(name string,
	tagKeys ...string) (shared.MetricID, shared.MetricsResult) {
	var metricID C.size_t
	result := C.envoy_dynamic_module_callback_access_logger_config_define_histogram(
		h.configPtr,
		stringToModuleBuffer(name),
		&metricID,
	)
	runtime.KeepAlive(name)
	return shared.MetricID(metricID), shared.MetricsResult(result)
}

// dymAccessLoggerHandle wraps the per-log-event Envoy pointer.
// Implements shared.AccessLoggerHandle. Created on the stack per log event; NOT pooled.
// The envoyPtr is valid only for the duration of on_access_logger_log.
type dymAccessLoggerHandle struct {
	envoyPtr C.envoy_dynamic_module_type_access_logger_envoy_ptr
}

func (h *dymAccessLoggerHandle) GetTimingInfo() shared.TimingInfo {
	var out C.envoy_dynamic_module_type_timing_info
	C.envoy_dynamic_module_callback_access_logger_get_timing_info(h.envoyPtr, &out)
	return shared.TimingInfo{
		StartTimeUnixNs:               int64(out.start_time_unix_ns),
		RequestCompleteDurationNs:     int64(out.request_complete_duration_ns),
		FirstUpstreamTxByteSentNs:     int64(out.first_upstream_tx_byte_sent_ns),
		LastUpstreamTxByteSentNs:      int64(out.last_upstream_tx_byte_sent_ns),
		FirstUpstreamRxByteReceivedNs: int64(out.first_upstream_rx_byte_received_ns),
		LastUpstreamRxByteReceivedNs:  int64(out.last_upstream_rx_byte_received_ns),
		FirstDownstreamTxByteSentNs:   int64(out.first_downstream_tx_byte_sent_ns),
		LastDownstreamTxByteSentNs:    int64(out.last_downstream_tx_byte_sent_ns),
	}
}

func (h *dymAccessLoggerHandle) GetBytesInfo() shared.BytesInfo {
	var out C.envoy_dynamic_module_type_bytes_info
	C.envoy_dynamic_module_callback_access_logger_get_bytes_info(h.envoyPtr, &out)
	return shared.BytesInfo{
		BytesReceived:     uint64(out.bytes_received),
		BytesSent:         uint64(out.bytes_sent),
		WireBytesReceived: uint64(out.wire_bytes_received),
		WireBytesSent:     uint64(out.wire_bytes_sent),
	}
}

func (h *dymAccessLoggerHandle) GetResponseFlags() uint64 {
	return uint64(C.envoy_dynamic_module_callback_access_logger_get_response_flags(h.envoyPtr))
}

func (h *dymAccessLoggerHandle) GetResponseCode() uint32 {
	return uint32(C.envoy_dynamic_module_callback_access_logger_get_response_code(h.envoyPtr))
}

func (h *dymAccessLoggerHandle) GetAttributeString(
	id shared.AttributeID,
) (shared.UnsafeEnvoyBuffer, bool) {
	var valueView C.envoy_dynamic_module_type_envoy_buffer
	ret := C.envoy_dynamic_module_callback_access_logger_get_attribute_string(
		h.envoyPtr,
		(C.envoy_dynamic_module_type_attribute_id)(id),
		&valueView,
	)
	if !bool(ret) || valueView.ptr == nil || valueView.length == 0 {
		return shared.UnsafeEnvoyBuffer{}, false
	}
	return envoyBufferToUnsafeEnvoyBuffer(valueView), true
}

func (h *dymAccessLoggerHandle) GetAttributeInt(
	id shared.AttributeID,
) (int64, bool) {
	var value C.uint64_t
	ret := C.envoy_dynamic_module_callback_access_logger_get_attribute_int(
		h.envoyPtr,
		(C.envoy_dynamic_module_type_attribute_id)(id),
		&value,
	)
	if !bool(ret) {
		return 0, false
	}
	return int64(value), true
}

func (h *dymAccessLoggerHandle) GetAttributeBool(
	id shared.AttributeID,
) (bool, bool) {
	var value C.bool
	ret := C.envoy_dynamic_module_callback_access_logger_get_attribute_bool(
		h.envoyPtr,
		(C.envoy_dynamic_module_type_attribute_id)(id),
		&value,
	)
	if !bool(ret) {
		return false, false
	}
	return bool(value), true
}

func (h *dymAccessLoggerHandle) GetHeader(
	headerType shared.HttpHeaderType, key string,
) (shared.UnsafeEnvoyBuffer, bool) {
	keyBuf := stringToModuleBuffer(key)
	var valueView C.envoy_dynamic_module_type_envoy_buffer
	ret := C.envoy_dynamic_module_callback_access_logger_get_header_value(
		h.envoyPtr,
		(C.envoy_dynamic_module_type_http_header_type)(headerType),
		keyBuf,
		&valueView,
		0,   // index: first value
		nil, // total_count_out: not needed
	)
	runtime.KeepAlive(key)
	if !bool(ret) {
		return shared.UnsafeEnvoyBuffer{}, false
	}
	return envoyBufferToUnsafeEnvoyBuffer(valueView), true
}

func (h *dymAccessLoggerHandle) GetWorkerIndex() uint32 {
	return uint32(C.envoy_dynamic_module_callback_access_logger_get_worker_index(h.envoyPtr))
}

func (h *dymAccessLoggerHandle) Log(level shared.LogLevel, format string, args ...any) {
	hostLog(level, format, args)
}

// =============================================================================
// ABI export functions -- called by Envoy
// =============================================================================

//export envoy_dynamic_module_on_access_logger_config_new
func envoy_dynamic_module_on_access_logger_config_new(
	configEnvoyPtr C.envoy_dynamic_module_type_access_logger_config_envoy_ptr,
	name C.envoy_dynamic_module_type_envoy_buffer,
	config C.envoy_dynamic_module_type_envoy_buffer,
) C.envoy_dynamic_module_type_access_logger_config_module_ptr {
	nameStr := envoyBufferToStringUnsafe(name)
	configBytes := envoyBufferToBytesUnsafe(config)

	handle := &dymAccessLoggerConfigHandle{configPtr: configEnvoyPtr}

	factory, err := sdk.NewAccessLoggerFactory(handle, nameStr, configBytes)
	if err != nil {
		hostLog(shared.LogLevelError, "access_logger: config_new failed name=%s err=%s", []any{nameStr, err.Error()})
		return nil
	}

	wrapper := &accessLoggerConfigWrapper{
		factory:      factory,
		configHandle: handle,
	}
	return (C.envoy_dynamic_module_type_access_logger_config_module_ptr)(
		accessLoggerConfigManager.record(wrapper))
}

//export envoy_dynamic_module_on_access_logger_config_destroy
func envoy_dynamic_module_on_access_logger_config_destroy(
	configModulePtr C.envoy_dynamic_module_type_access_logger_config_module_ptr,
) {
	ptr := unsafe.Pointer(configModulePtr)
	wrapper := accessLoggerConfigManager.unwrap(ptr)
	if wrapper == nil {
		return
	}
	wrapper.factory.OnDestroy()
	accessLoggerConfigManager.remove(ptr)
}

//export envoy_dynamic_module_on_access_logger_new
func envoy_dynamic_module_on_access_logger_new(
	configModulePtr C.envoy_dynamic_module_type_access_logger_config_module_ptr,
	_ C.envoy_dynamic_module_type_access_logger_envoy_ptr,
) C.envoy_dynamic_module_type_access_logger_module_ptr {
	configPtr := unsafe.Pointer(configModulePtr)
	configWrapper := accessLoggerConfigManager.unwrap(configPtr)
	if configWrapper == nil {
		return nil
	}

	logger := configWrapper.factory.NewLogger()
	if logger == nil {
		return nil
	}

	wrapper := &accessLoggerWrapper{
		logger:    logger,
		configPtr: configPtr,
	}
	return (C.envoy_dynamic_module_type_access_logger_module_ptr)(
		accessLoggerManager.record(wrapper))
}

//export envoy_dynamic_module_on_access_logger_log
func envoy_dynamic_module_on_access_logger_log(
	loggerEnvoyPtr C.envoy_dynamic_module_type_access_logger_envoy_ptr,
	loggerModulePtr C.envoy_dynamic_module_type_access_logger_module_ptr,
	logType C.envoy_dynamic_module_type_access_log_type,
) {
	wrapper := accessLoggerManager.unwrap(unsafe.Pointer(loggerModulePtr))
	if wrapper == nil {
		return
	}
	// Stack-allocate the handle: it is only valid for this call and must not escape.
	handle := dymAccessLoggerHandle{envoyPtr: loggerEnvoyPtr}
	wrapper.logger.OnLog(&handle, shared.AccessLogType(logType))
}

//export envoy_dynamic_module_on_access_logger_destroy
func envoy_dynamic_module_on_access_logger_destroy(
	loggerModulePtr C.envoy_dynamic_module_type_access_logger_module_ptr,
) {
	ptr := unsafe.Pointer(loggerModulePtr)
	wrapper := accessLoggerManager.unwrap(ptr)
	if wrapper == nil {
		return
	}
	wrapper.logger.OnDestroy()
	accessLoggerManager.remove(ptr)
}

//export envoy_dynamic_module_on_access_logger_flush
func envoy_dynamic_module_on_access_logger_flush(
	loggerModulePtr C.envoy_dynamic_module_type_access_logger_module_ptr,
) {
	wrapper := accessLoggerManager.unwrap(unsafe.Pointer(loggerModulePtr))
	if wrapper == nil {
		return
	}
	// Optional: if the logger implements Flusher, call it.
	type flusher interface{ Flush() }
	if f, ok := wrapper.logger.(flusher); ok {
		f.Flush()
	}
}
