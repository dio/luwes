package llmproxy

import "bytes"

// scanModel scans raw JSON bytes for the value of the "model" key.
// Returns a slice pointing into body (zero-copy, zero-alloc).
// Returns nil if the key is not found, empty non-nil slice if value is "".
// Does NOT handle deeply nested JSON: only top-level keys.
func scanModel(body []byte) []byte {
	if len(body) == 0 {
		return nil
	}

	const key = `"model"`
	idx := bytes.Index(body, []byte(key))
	if idx < 0 {
		return nil
	}

	// Advance past the key.
	rest := body[idx+len(key):]

	// Skip whitespace and colon.
	i := 0
	for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t' || rest[i] == '\n' || rest[i] == '\r') {
		i++
	}
	if i >= len(rest) || rest[i] != ':' {
		return nil
	}
	i++ // skip colon

	// Skip whitespace before value.
	for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t' || rest[i] == '\n' || rest[i] == '\r') {
		i++
	}
	if i >= len(rest) || rest[i] != '"' {
		return nil
	}
	i++ // skip opening quote

	// Scan to closing quote, handling backslash escapes.
	start := i
	for i < len(rest) {
		c := rest[i]
		if c == '\\' {
			i += 2 // skip escaped char
			continue
		}
		if c == '"' {
			return rest[start:i] // zero-copy slice into body
		}
		i++
	}
	return nil
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
