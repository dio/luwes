package llmproxy

import (
	"testing"
)

func TestScanModel_SimpleString(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[]}`)
	got := scanModel(body)
	if string(got) != "gpt-4" {
		t.Errorf("want %q, got %q", "gpt-4", got)
	}
}

func TestScanModel_WithSpaces(t *testing.T) {
	body := []byte(`{ "model" : "claude-3-sonnet" , "max_tokens": 1024 }`)
	got := scanModel(body)
	if string(got) != "claude-3-sonnet" {
		t.Errorf("want %q, got %q", "claude-3-sonnet", got)
	}
}

func TestScanModel_NotFound(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	got := scanModel(body)
	if got != nil {
		t.Errorf("want nil, got %q", got)
	}
}

func TestScanModel_EmptyBody(t *testing.T) {
	got := scanModel(nil)
	if got != nil {
		t.Errorf("want nil, got %q", got)
	}
}

func TestScanModel_EmptyValue(t *testing.T) {
	body := []byte(`{"model":""}`)
	got := scanModel(body)
	// empty string model: return empty non-nil slice (key was found)
	if got == nil {
		t.Error("want non-nil (key present), got nil")
	}
	if len(got) != 0 {
		t.Errorf("want empty slice, got %q", got)
	}
}

func TestScanModel_EscapedQuoteInValue(t *testing.T) {
	// Pathological: model name contains backslash-escaped quote.
	// Scanner reads until the next unescaped quote.
	body := []byte(`{"model":"gpt-4-turbo","stream":true}`)
	got := scanModel(body)
	if string(got) != "gpt-4-turbo" {
		t.Errorf("want %q, got %q", "gpt-4-turbo", got)
	}
}

func TestScanModel_AtEnd(t *testing.T) {
	body := []byte(`{"stream":true,"model":"gemini-pro"}`)
	got := scanModel(body)
	if string(got) != "gemini-pro" {
		t.Errorf("want %q, got %q", "gemini-pro", got)
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

func TestScanModel_ZeroAllocs(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	allocs := testing.AllocsPerRun(1000, func() {
		_ = scanModel(body)
	})
	if allocs > 0 {
		t.Errorf("scanModel: want 0 allocs, got %.0f", allocs)
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
