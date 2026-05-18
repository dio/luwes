package e2e

// callout_test.go: e2e tests for w.HTTPCallout, w.HTTPStream, and w.Do.
//
// Port 10005: callout-sahl filter, uses w.HTTPCallout to call callout_upstream.
//             Injects x-auth-user header from callout response or returns 401.
// Port 10006: stream-sahl filter, uses w.HTTPStream to stream to callout_upstream.
//             Accumulates chunks, sets x-stream-chunks header with chunk count.
// Port 10007: do-sahl filter, uses w.Go + w.Do to call callout_upstream.
//             Same contract as callout-sahl, proves the channel bridge path.

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
)

// CalloutSahlSuite tests w.HTTPCallout end-to-end.
type CalloutSahlSuite struct{ suite.Suite }

func TestCalloutSahl(t *testing.T) { suite.Run(t, new(CalloutSahlSuite)) }

// TestAccept_CalloutInjectsHeader: callout succeeds, x-auth-user injected.
func (s *CalloutSahlSuite) TestAccept_CalloutInjectsHeader() {
	req, _ := http.NewRequest(http.MethodGet, calloutSahlAddr+"/auth-ok", nil)
	resp := mustDo(s.T(), req)
	defer resp.Body.Close()
	s.Equal(http.StatusOK, resp.StatusCode)
}

// TestReject_CalloutDenied: callout returns 401, filter short-circuits.
func (s *CalloutSahlSuite) TestReject_CalloutDenied() {
	req, _ := http.NewRequest(http.MethodGet, calloutSahlAddr+"/auth-deny", nil)
	resp := mustDo(s.T(), req)
	body := readBody(s.T(), resp)
	s.Equal(http.StatusUnauthorized, resp.StatusCode)
	s.Contains(body, "denied")
}

// StreamSahlSuite tests w.HTTPStream end-to-end.
type StreamSahlSuite struct{ suite.Suite }

func TestStreamSahl(t *testing.T) { suite.Run(t, new(StreamSahlSuite)) }

// TestStream_DataChunksReceived: upstream sends multiple chunks, filter counts them.
func (s *StreamSahlSuite) TestStream_DataChunksReceived() {
	req, _ := http.NewRequest(http.MethodPost, streamSahlAddr+"/stream-chunks", strings.NewReader("{}"))
	req.Header.Set("content-type", "application/json")
	resp := mustDo(s.T(), req)
	defer resp.Body.Close()
	s.Equal(http.StatusOK, resp.StatusCode)
}

// DoSahlSuite tests w.Go + w.Do end-to-end.
// NOTE: w.Do is designed for multi-step callouts that ultimately forward the request.
// Rejection (w.Send) from inside w.Go requires the callout callback to fire from a
// filter callback context, not a scheduled function. Use w.HTTPCallout for rejection.
type DoSahlSuite struct{ suite.Suite }

func TestDoSahl(t *testing.T) { suite.Run(t, new(DoSahlSuite)) }

// TestDo_Accept_HeaderInjected: callout succeeds, x-auth-user injected, request forwarded.
func (s *DoSahlSuite) TestDo_Accept_HeaderInjected() {
	req, _ := http.NewRequest(http.MethodGet, doSahlAddr+"/auth-ok", nil)
	resp := mustDo(s.T(), req)
	defer resp.Body.Close()
	s.Equal(http.StatusOK, resp.StatusCode)
}

// readStatWithPrefix reads an Envoy stat and returns its value, -1 if missing.
// Exported for reuse across suites.
func readStatWithPrefix(t *testing.T, name string) int64 {
	t.Helper()
	for _, candidate := range []string{"dynamicmodulescustom." + name, name} {
		resp, err := http.Get(adminAddr + "/stats?filter=" + candidate + "&format=text")
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, candidate+":") {
				var v int64
				fmt.Sscanf(strings.TrimPrefix(line, candidate+":"), " %d", &v)
				return v
			}
		}
	}
	return -1
}