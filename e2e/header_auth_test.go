package e2e

import (
	"net/http"
	"testing"
)

func TestHeaderAuth_Accept(t *testing.T) {
	req, _ := http.NewRequest("GET", envoyAddr+"/", nil)
	req.Header.Set("x-api-key", "secret-key-abc")

	resp := mustDo(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestHeaderAuth_Reject_MissingKey(t *testing.T) {
	req, _ := http.NewRequest("GET", envoyAddr+"/", nil)

	resp := mustDo(t, req)
	body := readBody(t, resp)

	if resp.StatusCode != 401 {
		t.Fatalf("expected 401, got %d (body: %s)", resp.StatusCode, body)
	}
	if body != `{"error":"missing x-api-key"}` {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestHeaderAuth_UserIDInjected(t *testing.T) {
	// The filter injects x-user-id = value of x-api-key.
	// The direct_response doesn't echo headers, but we can verify the 200
	// (meaning the filter continued rather than rejected).
	req, _ := http.NewRequest("GET", envoyAddr+"/", nil)
	req.Header.Set("x-api-key", "user-token-xyz")

	resp := mustDo(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}
