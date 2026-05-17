package sahl

import (
	"fmt"
	"sync"

	"github.com/dio/luwes/shared"
)

// MetricID is an opaque handle to an Envoy metric defined at config time.
// Re-exported from shared so callers do not need to import shared directly.
type MetricID = shared.MetricID

// ConfigHandle is passed to factory functions at filter config creation time.
// Use it to define Envoy metrics and read raw filter config bytes.
// Methods must only be called from the factory, not from request handlers.
type ConfigHandle interface {
	// DefineCounter defines an Envoy counter metric with the given name and tag keys.
	DefineCounter(name string, tagKeys ...string) (MetricID, error)

	// DefineHistogram defines an Envoy histogram with the given name and tag keys.
	DefineHistogram(name string, tagKeys ...string) (MetricID, error)

	// RawConfig returns the raw filter_config bytes from envoy.yaml.
	// Returns nil if no filter_config was provided.
	RawConfig() []byte

	// Log emits a message to Envoy's logger.
	Log(level shared.LogLevel, format string, args ...any)
}

// configHandleImpl wraps shared.HttpFilterConfigHandle to implement ConfigHandle.
type configHandleImpl struct {
	h   shared.HttpFilterConfigHandle
	raw []byte
}

func (c *configHandleImpl) DefineCounter(name string, tagKeys ...string) (MetricID, error) {
	id, res := c.h.DefineCounter(name, tagKeys...)
	if res != shared.MetricsSuccess {
		return 0, fmt.Errorf("sahl: DefineCounter %q failed (result=%d)", name, res)
	}
	return id, nil
}

func (c *configHandleImpl) DefineHistogram(name string, tagKeys ...string) (MetricID, error) {
	id, res := c.h.DefineHistogram(name, tagKeys...)
	if res != shared.MetricsSuccess {
		return 0, fmt.Errorf("sahl: DefineHistogram %q failed (result=%d)", name, res)
	}
	return id, nil
}

func (c *configHandleImpl) RawConfig() []byte { return c.raw }

func (c *configHandleImpl) Log(level shared.LogLevel, format string, args ...any) {
	c.h.Log(level, format, args...)
}

// HandlerFunc is a synchronous request handler. It runs on the Envoy worker
// thread and must not block. For blocking work, call [Writer.Go] which upgrades
// the request to goroutine mode.
type HandlerFunc func(w *Writer, r *Request)

// Middleware wraps a HandlerFunc, returning a new HandlerFunc.
//
//	func LoggingMiddleware(next sahl.HandlerFunc) sahl.HandlerFunc {
//	    return func(w *sahl.Writer, r *sahl.Request) {
//	        r.Log(sahl.LogInfo, "request: %s", r.Path)
//	        next(w, r)
//	    }
//	}
type Middleware func(HandlerFunc) HandlerFunc

// Chain composes middlewares around a handler. Execution order is right to left:
// Chain(h, m1, m2) executes as m2 → m1 → h.
func Chain(h HandlerFunc, mw ...Middleware) HandlerFunc {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// HandlerFactory is a constructor called once at filter config creation time.
// It receives the ConfigHandle (for metrics, raw config) and returns a HandlerFunc
// bound to that config instance. Use this when the handler needs per-config state.
type HandlerFactory func(h ConfigHandle) (HandlerFunc, error)

// ConfigFunc is a side-effect-only config function. Called once at filter config
// creation time. Return a non-nil error to abort filter loading.
type ConfigFunc func(h ConfigHandle) error

// -- registry --

var (
	registryMu sync.Mutex
	registry   = make(map[string]*filterDef)
)

type filterDef struct {
	configFn   ConfigFunc
	factoryFn  HandlerFactory
	handler    HandlerFunc
	responseFn ResponseHandlerFunc
}

// Register registers a synchronous filter handler by name.
// Call from your filter package's init().
func Register(name string, h HandlerFunc) {
	mustAdd(name, &filterDef{handler: h})
}

// RegisterWithConfig registers a filter with a one-time config setup function
// and a handler. configFn defines metrics and parses config; handler handles requests.
// Both share package-level state. For per-config isolation use [RegisterFactory].
func RegisterWithConfig(name string, configFn ConfigFunc, h HandlerFunc) {
	mustAdd(name, &filterDef{configFn: configFn, handler: h})
}

// RegisterWithResponse registers a filter with both a request handler and a
// response observer. The response handler is called on each response body chunk
// (observe mode: zero added latency, body always forwarded downstream).
// Use it for SSE tapping, token counting, response header inspection.
//
// The response handler is also called once on response headers (Data=nil,
// EndStream=false) to allow Content-Type-gated setup.
func RegisterWithResponse(name string, h HandlerFunc, resp ResponseHandlerFunc) {
	mustAdd(name, &filterDef{handler: h, responseFn: resp})
}

// RegisterWithConfigAndResponse is like [RegisterWithConfig] but also registers
// a response observer.
func RegisterWithConfigAndResponse(name string, configFn ConfigFunc, h HandlerFunc, resp ResponseHandlerFunc) {
	mustAdd(name, &filterDef{configFn: configFn, handler: h, responseFn: resp})
}

// RegisterFactory registers a filter using a factory function that returns a
// HandlerFunc per filter config instance. Each Envoy listener using this filter
// gets its own HandlerFunc with isolated config state.
func RegisterFactory(name string, factoryFn HandlerFactory) {
	mustAdd(name, &filterDef{factoryFn: factoryFn})
}

func mustAdd(name string, def *filterDef) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("BUG: sahl filter %q registered twice", name))
	}
	registry[name] = def
}

// Factory wraps a HandlerFunc as a shared.HttpFilterConfigFactory for direct
// use with sdk.Register:
//
//	sdk.Register("my-filter", sahl.Factory(myHandler))
//
// Use this instead of sahl.Register when you are registering via the luwes
// SDK registry directly.
func Factory(h HandlerFunc) shared.HttpFilterConfigFactory {
	return newConfigFactory("", &filterDef{handler: h})
}

// Factories returns all registered sahl filters as a map of
// shared.HttpFilterConfigFactory, suitable for sdk.RegisterHttpFilterConfigFactories.
//
// Call once from cmd/main.go init():
//
//	sdk.RegisterHttpFilterConfigFactories(sahl.Factories())
func Factories() map[string]shared.HttpFilterConfigFactory {
	registryMu.Lock()
	defer registryMu.Unlock()
	out := make(map[string]shared.HttpFilterConfigFactory, len(registry))
	for name, def := range registry {
		out[name] = newConfigFactory(name, def)
	}
	return out
}
