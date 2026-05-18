package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/dio/luwes/e2e/accessloggersink"
)

func TestAccessLogger_FinalizedFields(t *testing.T) {
	accessloggersink.Reset()

	resp, err := http.Get(accessLoggerAddr + "/")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	entries := accessloggersink.Drain(5 * time.Second)
	if len(entries) == 0 {
		t.Fatal("no access log entries received within 5s")
	}

	e := entries[0]

	// The log type for a completed HTTP request is DownstreamEnd (6).
	if e.LogType != 6 {
		t.Errorf("expected log type DownstreamEnd (6), got %d", e.LogType)
	}

	// Duration must be finalized and non-zero.
	if e.DurationMs <= 0 {
		t.Errorf("expected duration_ms > 0, got %f", e.DurationMs)
	}

	// Envoy sends the response body to the client, so bytes_sent must be > 0.
	if e.BytesSent == 0 {
		t.Errorf("expected bytes_sent > 0, got %d", e.BytesSent)
	}

	// Direct response returns 200.
	if e.ResponseCode != 200 {
		t.Errorf("expected response code 200, got %d", e.ResponseCode)
	}

	// code_details is set by Envoy for every completed stream.
	if e.CodeDetails == "" {
		t.Errorf("expected non-empty code_details, got empty string")
	}
}
