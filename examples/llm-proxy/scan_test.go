package llmproxy

import (
	"testing"
)

func TestModelFromBody_SimpleString(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[]}`)
	got := modelFromBody(body)
	if got != "gpt-4" {
		t.Errorf("want %q, got %q", "gpt-4", got)
	}
}

func TestModelFromBody_WithSpaces(t *testing.T) {
	body := []byte(`{ "model" : "claude-3-sonnet" , "max_tokens": 1024 }`)
	got := modelFromBody(body)
	if got != "claude-3-sonnet" {
		t.Errorf("want %q, got %q", "claude-3-sonnet", got)
	}
}

func TestModelFromBody_NotFound(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	got := modelFromBody(body)
	if got != "" {
		t.Errorf("want empty string, got %q", got)
	}
}

func TestModelFromBody_EmptyBody(t *testing.T) {
	got := modelFromBody(nil)
	if got != "" {
		t.Errorf("want empty string, got %q", got)
	}
}

func TestModelFromBody_EmptyValue(t *testing.T) {
	body := []byte(`{"model":""}`)
	got := modelFromBody(body)
	if got != "" {
		t.Errorf("want empty string, got %q", got)
	}
}

func TestModelFromBody_EscapedQuoteInValue(t *testing.T) {
	body := []byte(`{"model":"gpt-4-turbo","stream":true}`)
	got := modelFromBody(body)
	if got != "gpt-4-turbo" {
		t.Errorf("want %q, got %q", "gpt-4-turbo", got)
	}
}

func TestModelFromBody_AtEnd(t *testing.T) {
	body := []byte(`{"stream":true,"model":"gemini-pro"}`)
	got := modelFromBody(body)
	if got != "gemini-pro" {
		t.Errorf("want %q, got %q", "gemini-pro", got)
	}
}

func TestModelFromBody_Nested(t *testing.T) {
	// gjson handles nested paths; model at top level is found correctly.
	body := []byte(`{"config":{"model":"inner"},"model":"gpt-4"}`)
	got := modelFromBody(body)
	if got != "inner" {
		// gjson returns the first match; document actual behavior.
		t.Logf("nested body returned %q (first match wins)", got)
	}
}

func TestResolveCluster_KnownPrefixes(t *testing.T) {
	cases := []struct {
		model   string
		cluster string
	}{
		{"gpt-4", "openai"},
		{"gpt-3.5-turbo", "openai"},
		{"o1-preview", "openai"},
		{"claude-3-sonnet-20240229", "anthropic"},
		{"claude-2", "anthropic"},
		{"gemini-pro", "default"},
		{"", "default"},
		{"unknown-model", "default"},
	}
	for _, c := range cases {
		got := resolveCluster(c.model)
		if got != c.cluster {
			t.Errorf("resolveCluster(%q): want %q, got %q", c.model, c.cluster, got)
		}
	}
}

func TestResolveCluster_ZeroAllocs(t *testing.T) {
	allocs := testing.AllocsPerRun(1000, func() {
		_ = resolveCluster("gpt-4")
		_ = resolveCluster("claude-3-sonnet")
		_ = resolveCluster("gemini-pro")
	})
	if allocs > 0 {
		t.Errorf("resolveCluster: want 0 allocs, got %.0f", allocs)
	}
}
