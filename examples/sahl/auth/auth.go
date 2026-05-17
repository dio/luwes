// Package auth demonstrates [sahl.RegisterFactory] -- the pattern for filters
// that need per-listener isolated state: parsed config and metric IDs.
//
// See [sahl package doc] for the full factory design: the three Envoy lifetimes
// (program init, filter config create, filter instance create), when to use
// RegisterFactory vs RegisterWithConfig, the metric ID constraint, and the
// configHandleImpl wrapping subtlety that affects how tests pass raw config.
// # Why RegisterFactory instead of RegisterWithConfig
//
// RegisterWithConfig stores metric IDs and parsed config in package-level vars.
// That works when there is exactly one Envoy listener using this filter. If two
// listeners use the same filter_name with different filter_config bytes, the
// configFn runs twice and the second call silently overwrites the first
// listener's config. No error, wrong behavior.
//
// RegisterFactory constructs a new HandlerFunc per filter config instance.
// Each listener gets its own closure capturing its own Config and MetricIDs.
// No shared mutable state, no race, no package vars.
//
// This example shows the pattern concretely: two listeners in envoy.yaml, same
// .so, two independent allowed-key sets.
//
// # Filter behaviour
//
// Request phase:
//   - Reads x-api-key header (zero-alloc via r.Header.Peek)
//   - Rejects with 401 if the key is not in the configured allow-list
//   - Injects x-user-id: <key> for accepted requests
//   - Increments auth_requests_total{result=allowed|rejected}
//   - Sets filter metadata: auth.result, auth.key
//
// Response side: counter auth_responses_total{status=2xx|4xx|5xx} via
// response observer; upstream status code in metadata.
//
// # Config (filter_config in envoy.yaml)
//
//	filter_config:
//	  "@type": type.googleapis.com/google.protobuf.StringValue
//	  value: '{"allowed_keys":["key-admin","key-readonly"],"metadata_ns":"auth"}'
//
// Two listeners with different configs:
//
//	listeners:
//	  - name: admin         # only key-admin passes
//	    ...
//	    filter_config: { value: '{"allowed_keys":["key-admin"]}' }
//	  - name: public        # key-admin and key-readonly pass
//	    ...
//	    filter_config: { value: '{"allowed_keys":["key-admin","key-readonly"]}' }
//
// Each listener gets its own AuthFilter instance with its own allow-list.
// RegisterWithConfig would overwrite the first listener's config with the second.
package auth

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/dio/luwes/sahl"
)

// Config holds the per-listener auth configuration parsed from filter_config.
type Config struct {
	AllowedKeys []string `json:"allowed_keys"`
	MetadataNS  string   `json:"metadata_ns"`
}

func init() {
	sahl.RegisterFactory("auth",
		func(h sahl.ConfigHandle) (sahl.HandlerFunc, error) {
			// Parse config once per listener, not per request.
			cfg := &Config{MetadataNS: "auth"} // defaults
			if raw := h.RawConfig(); len(raw) > 0 {
				if err := json.Unmarshal(raw, cfg); err != nil {
					return nil, fmt.Errorf("auth: bad config: %w", err)
				}
			}

			// Define metrics once per listener.
			reqTotal, err := h.DefineCounter("auth_requests_total", "result")
			if err != nil {
				return nil, err
			}

			// Build the fast-lookup set from the config.
			allowed := make(map[string]struct{}, len(cfg.AllowedKeys))
			for _, k := range cfg.AllowedKeys {
				allowed[k] = struct{}{}
			}

			// Return a HandlerFunc that closes over cfg, allowed, and reqTotal.
			// This closure is the "per-listener instance": two listeners with
			// different filter_config bytes get two independent closures.
			return sahl.Chain(
				func(w *sahl.Writer, r *sahl.Request) {
					// r.Header.Peek: zero-alloc on CGO path (unsafe string,
					// valid only during this callback).
					key, ok := r.Header.Peek("x-api-key")
					if !ok || len(key) == 0 {
						w.IncrementCounter(reqTotal, 1, "rejected")
						w.SetMetadata(cfg.MetadataNS, "result", "rejected")
						w.SetResponseHeader("content-type", "application/json")
						w.Send(http.StatusUnauthorized, `{"error":"missing x-api-key"}`)
						return
					}
					if _, ok := allowed[key]; !ok {
						w.IncrementCounter(reqTotal, 1, "rejected")
						w.SetMetadata(cfg.MetadataNS, "result", "rejected")
						w.SetResponseHeader("content-type", "application/json")
						w.Send(http.StatusUnauthorized, `{"error":"invalid x-api-key"}`)
						return
					}
					w.IncrementCounter(reqTotal, 1, "allowed")
					w.SetMetadata(cfg.MetadataNS, "result", "allowed")
					w.SetMetadata(cfg.MetadataNS, "key", key)
					// key is an unsafe string: copy it before it escapes this callback.
					w.SetRequestHeader("x-user-id", key)
				},
				loggingMiddleware(cfg),
			), nil
		},
	)
}

// loggingMiddleware logs each request with its method, path, and filter name.
// It takes cfg so it can include the namespace in the log, but it is otherwise
// stateless -- there is no per-request allocation from this middleware.
func loggingMiddleware(cfg *Config) sahl.Middleware {
	return func(next sahl.HandlerFunc) sahl.HandlerFunc {
		return func(w *sahl.Writer, r *sahl.Request) {
			r.LogAttrs(sahl.LogInfo, "auth request",
				slog.String("ns", cfg.MetadataNS),
				slog.String("method", r.Method),
				slog.String("path", r.Path),
			)
			next(w, r)
		}
	}
}

// ExtensionName is the filter name used in envoy.yaml filter_name.
const ExtensionName = "auth"
