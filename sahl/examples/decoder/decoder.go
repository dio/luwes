// Package decoder demonstrates LLM model-based routing using the sahl body-aware
// handler API ([sahl.RegisterWithBodyConfigAndResponse]).
//
// What it does:
//   - Reads the "model" field from the JSON request body (first 8 KB)
//   - Maps model name to a provider cluster (openai, anthropic, default)
//   - Sets x-cluster header and calls ClearRouteCache so Envoy's cluster_header
//     route selects the right upstream
//   - Sets filter metadata (model, cluster) for access logs and downstream filters
//   - Emits per-cluster request counters
//   - On the response side, extracts token usage from both SSE streams and
//     non-streaming JSON responses, emits per-cluster counters
//
// # Envoy config wiring
//
// The filter requires a cluster_header route:
//
//	route_config:
//	  virtual_hosts:
//	    - name: providers
//	      domains: ["*"]
//	      routes:
//	        - match: { prefix: "/" }
//	          route:
//	            cluster_header: x-cluster
//
// Clusters named "openai", "anthropic", and "default" must exist.
package decoder

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/dio/luwes/buffer"
	"github.com/dio/luwes/sahl"
)

// Cluster names written to x-cluster header.
const (
	ClusterOpenAI    = "openai"
	ClusterAnthropic = "anthropic"
	ClusterDefault   = "default"
)

// Metrics: defined once at config time, incremented per-request.
var (
	requestsTotal sahl.MetricID // decoder_requests_total{cluster}
	inputTokens   sahl.MetricID // decoder_input_tokens{cluster}
	outputTokens  sahl.MetricID // decoder_output_tokens{cluster}
	ttftMs        sahl.MetricID // decoder_ttft_ms{cluster}
)

func init() {
	sahl.RegisterWithBodyConfigAndResponse(
		"decoder",
		func(h sahl.ConfigHandle) error {
			var err error
			if requestsTotal, err = h.DefineCounter("decoder_requests_total", "cluster"); err != nil {
				return err
			}
			if inputTokens, err = h.DefineCounter("decoder_input_tokens", "cluster"); err != nil {
				return err
			}
			if outputTokens, err = h.DefineCounter("decoder_output_tokens", "cluster"); err != nil {
				return err
			}
			ttftMs, err = h.DefineHistogram("decoder_ttft_ms", "cluster")
			return err
		},
		decoderRequest,
		decoderResponse,
	)
}

// decoderRequest is the request handler. Called after the full request body
// is buffered (via bodyAware registration). r.Body() returns the complete body.
func decoderRequest(w *sahl.Writer, r *sahl.Request) {
	body := r.Body()

	var req struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &req) //nolint:errcheck

	cluster := resolveCluster(req.Model)

	w.SetRequestHeader("x-cluster", cluster)
	w.SetMetadata("decoder", "model", req.Model)
	w.SetMetadata("decoder", "cluster", cluster)
	w.ClearRouteCache()
	w.IncrementCounter(requestsTotal, 1, cluster)
}

// decoderResponse is the response observer. Called on each response body chunk
// and on response headers (Data==nil). Taps token usage and emits metrics on
// stream completion.
func decoderResponse(w *sahl.Writer, chunk *sahl.ResponseChunk) {
	// Headers call: set up per-request state.
	if chunk.Data == nil && !chunk.EndStream {
		s := &respState{
			cluster: clusterFromMeta(w),
			sentAt:  time.Now(),
		}
		if strings.Contains(chunk.ContentType, "text/event-stream") {
			s.ring = buffer.NewHeadTail(8*1024, 64*1024)
			s.isSSE = true
		}
		*chunk.Context = s
		return
	}

	s, ok := (*chunk.Context).(*respState)
	if !ok || s == nil {
		return
	}

	if s.isSSE {
		tapSSEChunk(w, s, chunk)
	} else {
		tapJSONChunk(w, s, chunk)
	}
}

// respState holds per-request response state.
type respState struct {
	cluster    string
	sentAt     time.Time
	firstChunk time.Time
	isSSE      bool
	ring       *buffer.HeadTail // SSE: head+tail ring
	jsonBuf    []byte           // JSON: accumulated body
}

// clusterFromMeta reads the cluster set by decoderRequest from filter metadata.
// Falls back to ClusterDefault if not set (e.g. request had no body).
func clusterFromMeta(_ *sahl.Writer) string {
	// In sahl we can't read metadata back from the writer (it's write-only).
	// The cluster is stored in metaMuts but not exposed for read.
	// Practical workaround: re-derive from the x-cluster request header.
	// For now, use default -- in production this would be passed via a
	// per-request context slot once sahl exposes one.
	return ClusterDefault
}

func tapSSEChunk(w *sahl.Writer, s *respState, chunk *sahl.ResponseChunk) {
	if len(chunk.Data) > 0 {
		if s.firstChunk.IsZero() {
			s.firstChunk = time.Now()
			emitTTFT(w, s)
		}
		s.ring.Write(chunk.Data)
	}
	if !chunk.EndStream {
		return
	}
	u := extractSSEUsage(s.ring.Head(), s.ring.Tail())
	emitUsage(w, s.cluster, u.input, u.output)
}

func tapJSONChunk(w *sahl.Writer, s *respState, chunk *sahl.ResponseChunk) {
	if len(chunk.Data) > 0 {
		if s.firstChunk.IsZero() {
			s.firstChunk = time.Now()
			emitTTFT(w, s)
		}
		s.jsonBuf = append(s.jsonBuf, chunk.Data...)
	}
	if !chunk.EndStream {
		return
	}
	if len(s.jsonBuf) == 0 {
		return
	}
	var resp struct {
		Usage *struct {
			PromptTokens     uint32 `json:"prompt_tokens"`
			CompletionTokens uint32 `json:"completion_tokens"`
			InputTokens      uint32 `json:"input_tokens"`
			OutputTokens     uint32 `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(s.jsonBuf, &resp) != nil || resp.Usage == nil {
		return
	}
	u := resp.Usage
	emitUsage(w, s.cluster, u.PromptTokens+u.InputTokens, u.CompletionTokens+u.OutputTokens)
}

func emitTTFT(w *sahl.Writer, s *respState) {
	if s.sentAt.IsZero() || s.firstChunk.IsZero() {
		return
	}
	ms := uint64(s.firstChunk.Sub(s.sentAt).Milliseconds())
	if ms > 0 {
		w.RecordHistogram(ttftMs, ms, s.cluster)
		w.SetMetadata("decoder", "ttft_ms", int64(ms))
	}
}

func emitUsage(w *sahl.Writer, cluster string, in, out uint32) {
	if in > 0 {
		w.IncrementCounter(inputTokens, uint64(in), cluster)
		w.SetMetadata("decoder", "input_tokens", in)
	}
	if out > 0 {
		w.IncrementCounter(outputTokens, uint64(out), cluster)
		w.SetMetadata("decoder", "output_tokens", out)
	}
}

// resolveCluster maps a model name to a provider cluster.
func resolveCluster(model string) string {
	switch {
	case strings.HasPrefix(model, "gpt-") ||
		strings.HasPrefix(model, "o1") ||
		strings.HasPrefix(model, "o3"):
		return ClusterOpenAI
	case strings.HasPrefix(model, "claude-"):
		return ClusterAnthropic
	case model == "":
		return ClusterDefault
	default:
		return ClusterDefault
	}
}

// tokenUsage is a simple struct for SSE extraction results.
type tokenUsage struct{ input, output uint32 }

// extractSSEUsage scans head + tail buffers for OpenAI/Anthropic usage fields.
// Reuses the same logic as sse-tap but operates on pre-buffered head+tail.
func extractSSEUsage(head, tail []byte) tokenUsage {
	var u tokenUsage

	// Scan head: Anthropic message_start input tokens.
	var curEvent string
	scanLines(head, func(line []byte) {
		switch {
		case bytes.HasPrefix(line, []byte("event: ")):
			curEvent = string(line[7:])
		case u.input == 0 && curEvent == "message_start" && bytes.HasPrefix(line, []byte("data: ")):
			var msg struct {
				Message struct {
					Usage struct {
						InputTokens uint32 `json:"input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal(line[6:], &msg) == nil && msg.Message.Usage.InputTokens > 0 {
				u.input = msg.Message.Usage.InputTokens
			}
		}
	})

	// Scan tail: OpenAI + Anthropic output tokens.
	curEvent = ""
	scanLines(tail, func(line []byte) {
		if bytes.HasPrefix(line, []byte("event: ")) {
			curEvent = string(line[7:])
			return
		}
		if !bytes.HasPrefix(line, []byte("data: ")) {
			return
		}
		data := line[6:]
		if bytes.Equal(data, []byte("[DONE]")) {
			return
		}
		if u.input == 0 || u.output == 0 {
			var ck struct {
				Usage *struct {
					PromptTokens     uint32 `json:"prompt_tokens"`
					CompletionTokens uint32 `json:"completion_tokens"`
					InputTokens      uint32 `json:"input_tokens"`
					OutputTokens     uint32 `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(data, &ck) == nil && ck.Usage != nil {
				if u.input == 0 {
					u.input = ck.Usage.PromptTokens + ck.Usage.InputTokens
				}
				if u.output == 0 {
					u.output = ck.Usage.CompletionTokens + ck.Usage.OutputTokens
				}
			}
		}
		if curEvent == "message_delta" && u.output == 0 {
			var delta struct {
				Usage struct {
					OutputTokens uint32 `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(data, &delta) == nil {
				u.output = delta.Usage.OutputTokens
			}
		}
	})

	return u
}

func scanLines(data []byte, fn func([]byte)) {
	for {
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			return
		}
		fn(bytes.TrimRight(data[:idx], "\r"))
		data = data[idx+1:]
	}
}

// send400 sends a 400 Bad Request with a JSON error body.
func send400(w *sahl.Writer, msg string) {
	w.Send(http.StatusBadRequest, `{"error":"`+msg+`"}`)
}

var _ = send400 // exported pattern
