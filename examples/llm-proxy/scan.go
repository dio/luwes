package llmproxy

import (
	"bytes"
	"unsafe"

	"github.com/tidwall/gjson"
)

// modelFromBody extracts the "model" field value from a JSON request body.
// Zero-alloc: unsafe.String converts the Envoy-owned []byte to a string without
// copying, and gjson.Get on a string input does not allocate for unescaped values.
// The returned string is a sub-slice of body's backing memory: valid only for
// the duration of the current Envoy callback.
func modelFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	s := unsafe.String(unsafe.SliceData(body), len(body))
	return gjson.Get(s, "model").Str
}

// routeEntry maps a model name prefix to an Envoy cluster name.
type routeEntry struct {
	prefix  []byte
	cluster string
}

// clusterRoutes is the static routing table.
// Entries are checked in order; first match wins.
// Add or reorder entries to change routing priority.
var clusterRoutes = [...]routeEntry{
	{[]byte("gpt-"), "openai"},
	{[]byte("o1"), "openai"},
	{[]byte("o3"), "openai"},
	{[]byte("claude-"), "anthropic"},
}

const clusterDefault = "default"

// resolveCluster returns the Envoy cluster name for the given model string.
// Zero-alloc: prefix scan on a static array, no map lookup, no string copy.
func resolveCluster(model string) string {
	b := []byte(model) // stack-allocated for short strings (< 32 bytes typical)
	for _, e := range clusterRoutes {
		if bytes.HasPrefix(b, e.prefix) {
			return e.cluster
		}
	}
	return clusterDefault
}
