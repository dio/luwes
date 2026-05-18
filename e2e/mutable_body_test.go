package e2e

// mutable_body_test.go: e2e tests for RegisterWithMutableResponse,
// w.SetResponseBody, and ResponseChunk.ResponseFlags.
//
// Port 10008: mutable-body-sahl filter routes:
//   GET /ok         -> callout_upstream (200, no response flags)
//   GET /infra-fail -> dead_upstream   (503, UF flag from Envoy)
//
// The filter replaces the response body with:
//   {"status":<code>,"flags":"<flags>"}
// This verifies both body mutation and flag population in one round-trip.

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type MutableBodySahlSuite struct{ suite.Suite }

func TestMutableBodySahl(t *testing.T) { suite.Run(t, new(MutableBodySahlSuite)) }

type mutBodyResp struct {
	Status int    `json:"status"`
	Flags  string `json:"flags"`
}

func (s *MutableBodySahlSuite) doMutableReq(path string) (int, mutBodyResp, http.Header) {
	s.T().Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, mutableBodySahlAddr+path, nil)
	s.Require().NoError(err)
	resp, err := client.Do(req)
	s.Require().NoError(err)
	defer resp.Body.Close()

	var out mutBodyResp
	// Best-effort JSON decode: may fail for Envoy-synthesized error bodies.
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, resp.Header
}

// TestMutableBody_SetResponseBody_ReplacesBody verifies that the filter's
// w.SetResponseBody call replaces the upstream response body with the
// JSON envelope. The mutated body is what the client receives.
func (s *MutableBodySahlSuite) TestMutableBody_SetResponseBody_ReplacesBody() {
	code, got, _ := s.doMutableReq("/ok")
	s.Equal(http.StatusOK, code)
	s.Equal(200, got.Status, "StatusCode in mutated body must be 200")
}

// TestMutableBody_ResponseFlags_EmptyForRealUpstream verifies that
// ResponseChunk.ResponseFlags is empty (or "-") when the upstream returns a
// real response with no infrastructure failure. Envoy uses "-" as the
// no-flags sentinel in %RESPONSE_FLAGS% format.
func (s *MutableBodySahlSuite) TestMutableBody_ResponseFlags_EmptyForRealUpstream() {
	_, got, _ := s.doMutableReq("/ok")
	// Envoy emits "-" when no flags are set (no infrastructure failure).
	s.True(got.Flags == "" || got.Flags == "-",
		"ResponseFlags must be empty or '-' for a real upstream 200 response, got %q", got.Flags)
}

// TestMutableBody_InfraFailure_Returns503 verifies that Envoy synthesizes a
// 503 when the upstream cluster is unreachable.
func (s *MutableBodySahlSuite) TestMutableBody_InfraFailure_Returns503() {
	code, _, _ := s.doMutableReq("/infra-fail")
	s.Equal(http.StatusServiceUnavailable, code, "unreachable upstream must produce 503")
}

// TestMutableBody_InfraFailure_HasResponseFlagsHeader verifies that Envoy adds
// x-envoy-response-flags to the downstream response for infrastructure failures.
// The UF flag (upstream connection failure) must be present.
func (s *MutableBodySahlSuite) TestMutableBody_InfraFailure_HasResponseFlagsHeader() {
	_, _, headers := s.doMutableReq("/infra-fail")
	flags := headers.Get("x-envoy-response-flags")
	s.NotEmpty(flags, "x-envoy-response-flags header must be present for infra failure")
	s.Contains(flags, "UF", "UF (upstream connection failure) flag must be set")
}
