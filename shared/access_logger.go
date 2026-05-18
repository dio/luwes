// Access logger ABI extension point for Envoy dynamic modules.
//
// The HTTP filter callbacks (OnRequestHeaders through OnStreamComplete) fire
// before Envoy finalizes stream attributes. Fields like request duration,
// response flags, response code details, and byte counts are only available
// after the stream completes. The access logger hook fires after that
// finalization, giving the module access to the complete stream record.
//
// Lifecycle relative to HTTP filter:
//
//	OnStreamComplete  (HTTP filter -- post-stream attrs NOT yet finalized)
//	  [Envoy finalizes StreamInfo]
//	on_access_logger_log  (access logger -- all attrs finalized)
//	  [access log flush]
//	OnDestroy  (HTTP filter -- safe to clean up correlation state here)
//
// See AccessLogger, AccessLoggerFactory, and AccessLoggerConfigFactory for
// the interfaces to implement. Register via luwes.RegisterAccessLogger.
package shared

// TimingInfo holds finalized stream timing from Envoy StreamInfo.
// All durations are in nanoseconds. A value of -1 means the timing is unavailable.
type TimingInfo struct {
	StartTimeUnixNs               int64
	RequestCompleteDurationNs     int64
	FirstUpstreamTxByteSentNs     int64
	LastUpstreamTxByteSentNs      int64
	FirstUpstreamRxByteReceivedNs int64
	LastUpstreamRxByteReceivedNs  int64
	FirstDownstreamTxByteSentNs   int64
	LastDownstreamTxByteSentNs    int64
}

// BytesInfo holds finalized byte counts from Envoy StreamInfo.
type BytesInfo struct {
	BytesReceived     uint64
	BytesSent         uint64
	WireBytesReceived uint64
	WireBytesSent     uint64
}

// AccessLogType identifies the type of access log event.
// Corresponds to envoy_dynamic_module_type_access_log_type in abi.h.
// For HTTP request completion, the relevant type is AccessLogTypeDownstreamEnd.
type AccessLogType int32

const (
	AccessLogTypeNotSet                                    AccessLogType = 0
	AccessLogTypeTcpUpstreamConnected                     AccessLogType = 1
	AccessLogTypeTcpPeriodic                              AccessLogType = 2
	AccessLogTypeTcpConnectionEnd                         AccessLogType = 3
	AccessLogTypeDownstreamStart                          AccessLogType = 4
	AccessLogTypeDownstreamPeriodic                       AccessLogType = 5
	AccessLogTypeDownstreamEnd                            AccessLogType = 6
	AccessLogTypeUpstreamPoolReady                        AccessLogType = 7
	AccessLogTypeUpstreamPeriodic                         AccessLogType = 8
	AccessLogTypeUpstreamEnd                              AccessLogType = 9
	AccessLogTypeDownstreamTunnelSuccessfullyEstablished  AccessLogType = 10
	AccessLogTypeUdpTunnelUpstreamConnected               AccessLogType = 11
	AccessLogTypeUdpPeriodic                              AccessLogType = 12
	AccessLogTypeUdpSessionEnd                            AccessLogType = 13
)

// HttpHeaderType identifies which header map to access in AccessLoggerHandle.GetHeader.
// Corresponds to envoy_dynamic_module_type_http_header_type in abi.h.
type HttpHeaderType int32

const (
	HttpHeaderTypeRequest          HttpHeaderType = 0
	HttpHeaderTypeRequestTrailer   HttpHeaderType = 1
	HttpHeaderTypeResponse         HttpHeaderType = 2
	HttpHeaderTypeResponseTrailer  HttpHeaderType = 3
)

// AccessLoggerHandle provides access to finalized stream state during OnLog.
// The handle is valid ONLY for the duration of the OnLog callback.
// Do not retain a reference to it after the callback returns.
type AccessLoggerHandle interface {
	// GetTimingInfo returns finalized stream timing. Durations are in nanoseconds; -1 = unavailable.
	GetTimingInfo() TimingInfo

	// GetBytesInfo returns finalized byte counts.
	GetBytesInfo() BytesInfo

	// GetResponseFlags returns Envoy response flags as a bitmask.
	// Individual flags: UF=1, UH=2, UT=4, LR=8, UR=16, UF=32, UC=64, DI=128, FI=256, RL=512, UAEX=1024...
	GetResponseFlags() uint64

	// GetResponseCode returns the finalized HTTP response code.
	GetResponseCode() uint32

	// GetAttributeString returns a finalized string attribute by ID.
	// Returns the buffer and true if available; zero buffer and false otherwise.
	GetAttributeString(id AttributeID) (UnsafeEnvoyBuffer, bool)

	// GetAttributeInt returns a finalized integer attribute by ID.
	GetAttributeInt(id AttributeID) (int64, bool)

	// GetAttributeBool returns a finalized bool attribute by ID.
	GetAttributeBool(id AttributeID) (bool, bool)

	// GetHeader retrieves a header value from the specified header map.
	// Returns the value buffer and true if the header exists.
	GetHeader(headerType HttpHeaderType, key string) (UnsafeEnvoyBuffer, bool)

	// GetWorkerIndex returns the worker index for this access log event.
	GetWorkerIndex() uint32

	// Log emits a message via Envoy's logging system.
	Log(level LogLevel, format string, args ...any)
}

// AccessLoggerConfigHandle is provided to AccessLoggerConfigFactory.Create on the main thread.
// Use it to define Envoy stats (counter, gauge, histogram) during initialization.
type AccessLoggerConfigHandle interface {
	// Log emits a message via Envoy's logging system.
	Log(level LogLevel, format string, args ...any)

	// DefineCounter creates a counter metric. Returns (id, result).
	// The id can be used with IncrementCounterValue on the AccessLoggerHandle.
	// Tag keys are optional; the order must match tag values supplied at increment time.
	DefineCounter(name string, tagKeys ...string) (MetricID, MetricsResult)

	// DefineGauge creates a gauge metric.
	DefineGauge(name string, tagKeys ...string) (MetricID, MetricsResult)

	// DefineHistogram creates a histogram metric.
	DefineHistogram(name string, tagKeys ...string) (MetricID, MetricsResult)
}

// AccessLogger is the per-worker-thread logger instance.
// Envoy creates one per worker thread via AccessLoggerFactory.NewLogger.
type AccessLogger interface {
	// OnLog is called for each access log event.
	// handle is valid only for the duration of this call; do not retain it.
	OnLog(handle AccessLoggerHandle, logType AccessLogType)

	// OnDestroy is called when this logger instance is being destroyed (worker shutdown).
	OnDestroy()
}

// AccessLoggerFactory creates AccessLogger instances, one per worker thread.
// Implementations must be thread-safe: NewLogger may be called concurrently.
type AccessLoggerFactory interface {
	// NewLogger creates a logger instance for one worker thread.
	NewLogger() AccessLogger

	// OnDestroy is called when the factory is being destroyed (config reload or shutdown).
	OnDestroy()
}

// AccessLoggerConfigFactory is created once on the main thread.
// It parses the access logger config and produces an AccessLoggerFactory.
type AccessLoggerConfigFactory interface {
	// Create is called on the main thread when the access logger config is loaded.
	// config is the raw JSON config from the Envoy YAML logger_config field.
	Create(handle AccessLoggerConfigHandle, config []byte) (AccessLoggerFactory, error)
}

// EmptyAccessLogger is a no-op base for access logger implementations.
// Embed it to avoid implementing unused methods.
type EmptyAccessLogger struct{}

func (e *EmptyAccessLogger) OnLog(_ AccessLoggerHandle, _ AccessLogType) {}
func (e *EmptyAccessLogger) OnDestroy()                                   {}
