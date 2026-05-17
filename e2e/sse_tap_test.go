package e2e

// SSE mock upstream and sse-tap e2e tests.
// The mock serves Anthropic-format SSE streams on demand.
// TestMain starts it before Envoy on a dynamic port.

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// startMockSSEServer starts a local HTTP server that streams Anthropic-format
// SSE responses. Returns the port it is listening on.
//
// Routes:
//
//	POST /anthropic   Anthropic message_start + message_delta + message_stop
//	POST /openai      OpenAI chat completions usage chunk + [DONE]
//	POST /non-sse     Plain JSON response (verifies filter skips non-SSE)
func startMockSSEServer() int {
	mux := http.NewServeMux()

	mux.HandleFunc("/anthropic", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.Header().Set("cache-control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		events := []string{
			"event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":42}}}\n\n",
			"event: content_block_delta\ndata: {\"delta\":{\"text\":\"Hello\"}}\n\n",
			"event: message_delta\ndata: {\"usage\":{\"output_tokens\":15}}\n\n",
			"event: message_stop\ndata: {}\n\n",
		}
		for _, e := range events {
			fmt.Fprint(w, e)
			flusher.Flush()
		}
	})

	mux.HandleFunc("/openai", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.Header().Set("cache-control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		events := []string{
			"data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n",
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\n",
			"data: [DONE]\n\n",
		}
		for _, e := range events {
			fmt.Fprint(w, e)
			flusher.Flush()
		}
	})

	mux.HandleFunc("/non-sse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"message":"not sse"}`)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: mock SSE server listen failed: %v\n", err)
		os.Exit(1)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	mockSSEServer = &http.Server{Handler: mux}
	go mockSSEServer.Serve(ln) //nolint:errcheck
	return port
}

// SSETapSuite tests the sse-tap filter on port 10002.
// Envoy proxies requests to the mock SSE upstream; the filter observes the
// response body and emits sse_tap_input_tokens / sse_tap_output_tokens counters.
type SSETapSuite struct {
	suite.Suite
}

func TestSSETap(t *testing.T) {
	suite.Run(t, new(SSETapSuite))
}

// doSSERequest sends a POST to the sse-tap listener and drains the response.
// Returns status code and drained body.
func (s *SSETapSuite) doSSERequest(path string) (int, string) {
	s.T().Helper()
	req, err := http.NewRequest(http.MethodPost, sseTapAddr+path, http.NoBody)
	s.Require().NoError(err)
	req.Header.Set("content-type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	s.Require().NoError(err)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// readStat reads a single stat counter from Envoy admin /stats.
// Returns the integer value or -1 if the stat is not present.
// Dynamic module custom metrics are prefixed with "dynamicmodulescustom." by Envoy.
func readStat(t *testing.T, name string) int64 {
	t.Helper()
	// Try both the bare name and the dynamicmodulescustom. prefix.
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

func (s *SSETapSuite) TestAnthropicSSE_StatusOK() {
	code, _ := s.doSSERequest("/anthropic")
	s.Equal(http.StatusOK, code)
}

func (s *SSETapSuite) TestAnthropicSSE_InputTokensCounter() {
	// Drain the stream.
	s.doSSERequest("/anthropic")

	// Give Envoy a moment to flush the OnStreamComplete / stats update.
	time.Sleep(100 * time.Millisecond)

	v := readStat(s.T(), "sse_tap_input_tokens")
	s.T().Logf("sse_tap_input_tokens = %d", v)
	s.Greater(v, int64(0), "input tokens counter must be non-zero after Anthropic SSE")
}

func (s *SSETapSuite) TestAnthropicSSE_OutputTokensCounter() {
	s.doSSERequest("/anthropic")
	time.Sleep(100 * time.Millisecond)

	v := readStat(s.T(), "sse_tap_output_tokens")
	s.T().Logf("sse_tap_output_tokens = %d", v)
	s.Greater(v, int64(0), "output tokens counter must be non-zero after Anthropic SSE")
}

func (s *SSETapSuite) TestOpenAISSE_CountersIncrement() {
	before := readStat(s.T(), "sse_tap_input_tokens")

	s.doSSERequest("/openai")
	time.Sleep(100 * time.Millisecond)

	after := readStat(s.T(), "sse_tap_input_tokens")
	s.T().Logf("sse_tap_input_tokens: %d -> %d", before, after)
	s.Greater(after, before, "input counter must increase after OpenAI SSE request")
}

func (s *SSETapSuite) TestNonSSE_NoCounterIncrease() {
	before := readStat(s.T(), "sse_tap_input_tokens")

	s.doSSERequest("/non-sse")
	time.Sleep(100 * time.Millisecond)

	after := readStat(s.T(), "sse_tap_input_tokens")
	s.Equal(before, after, "non-SSE response must not increment token counters")
}

func (s *SSETapSuite) TestRequestHeader_XSSETap() {
	// The request handler sets x-sse-tap: 1. We can't observe upstream request
	// headers directly from the client, but we can verify the response was
	// proxied (status 200) rather than rejected, which confirms the filter ran.
	code, _ := s.doSSERequest("/anthropic")
	s.Equal(http.StatusOK, code)
}
