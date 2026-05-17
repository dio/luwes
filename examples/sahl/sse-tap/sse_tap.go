// Package ssetap demonstrates how to tap an SSE response stream using the sahl
// response observer API ([sahl.RegisterWithConfigAndResponse]).
//
// This filter sits on the response path and extracts token usage from streaming
// LLM responses without buffering the entire body:
//
//   - Input tokens appear near the START of the stream (Anthropic message_start,
//     OpenAI first usage chunk).
//   - Output tokens appear near the END (message_delta, final usage chunk).
//
// The filter uses [buffer.HeadTail] to capture the first 8 KB and last 64 KB of
// each response. The middle of a large response is never stored. On stream
// completion (EndStream=true) it scans both regions and emits Envoy counters.
//
// The response observer runs on the Envoy worker thread with zero added latency:
// chunks are forwarded to the downstream client as they arrive (BodyStatusContinue).
package ssetap

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/dio/luwes/examples/sahl/sse-tap/buffer"
	"github.com/dio/luwes/sahl"
)

const ExtensionName = "sse-tap"

// TokenUsage holds extracted token counts from a streaming LLM response.
type TokenUsage struct {
	Input  uint32
	Output uint32
}

// Metrics defined once at config time via RegisterWithConfigAndResponse's configFn.
var (
	inputTokensID  sahl.MetricID
	outputTokensID sahl.MetricID
)

func init() {
	sahl.RegisterWithConfigAndResponse(
		ExtensionName,
		// configFn: define metrics once at filter config load time.
		func(h sahl.ConfigHandle) error {
			var err error
			inputTokensID, err = h.DefineCounter("sse_tap_input_tokens")
			if err != nil {
				return err
			}
			outputTokensID, err = h.DefineCounter("sse_tap_output_tokens")
			return err
		},
		// request handler: tag the request so upstream logs can correlate.
		func(w *sahl.Writer, _ *sahl.Request) {
			w.SetRequestHeader("x-sse-tap", "1")
		},
		// response observer: see tapResponse below.
		tapResponse,
	)
}

// ringState is per-request state for the response observer.
// Stored in chunk.Context across the header call and each body chunk call.
type ringState struct {
	ring *buffer.HeadTail
	skip bool
}

// tapResponse is the response observer. Called:
//   - Once on response headers (chunk.Data == nil, chunk.EndStream == false):
//     allocate ring state, check Content-Type.
//   - Once per body chunk: write into ring.
//   - Once with EndStream == true: parse ring and emit metrics.
func tapResponse(w *sahl.Writer, chunk *sahl.ResponseChunk) {
	if chunk.Data == nil {
		// Headers call: allocate per-request state.
		s := &ringState{}
		if !strings.Contains(chunk.ContentType, "text/event-stream") {
			s.skip = true
		} else {
			// 8 KB head captures message_start / early usage.
			// 64 KB tail captures message_delta / final usage chunk.
			s.ring = buffer.NewHeadTail(8*1024, 64*1024)
		}
		*chunk.Context = s
		return
	}

	s, ok := (*chunk.Context).(*ringState)
	if !ok || s.skip {
		return
	}

	s.ring.Write(chunk.Data)

	if !chunk.EndStream {
		return
	}

	u := ExtractUsage(s.ring.Head(), s.ring.Tail())
	// Context is zeroed by sahl on pool return; no explicit cleanup needed.

	if u.Input > 0 {
		w.IncrementCounter(inputTokensID, uint64(u.Input))
	}
	if u.Output > 0 {
		w.IncrementCounter(outputTokensID, uint64(u.Output))
	}
	w.SetMetadata("sse_tap", "input_tokens", u.Input)
	w.SetMetadata("sse_tap", "output_tokens", u.Output)
}

// ExtractUsage scans head for input tokens and tail for output tokens.
// Handles both OpenAI and Anthropic SSE formats.
// Exported for unit testing independently of Envoy.
func ExtractUsage(head, tail []byte) TokenUsage {
	var u TokenUsage

	// Scan head: Anthropic message_start (input tokens appear first).
	var curEvent string
	scanLines(head, func(line []byte) {
		switch {
		case bytes.HasPrefix(line, []byte("event: ")):
			curEvent = string(line[7:])
		case u.Input == 0 && curEvent == "message_start" && bytes.HasPrefix(line, []byte("data: ")):
			var msg struct {
				Message struct {
					Usage struct {
						InputTokens uint32 `json:"input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal(line[6:], &msg) == nil && msg.Message.Usage.InputTokens > 0 {
				u.Input = msg.Message.Usage.InputTokens
			}
		}
	})

	// Scan tail: both formats for output + OpenAI input (if not found in head).
	curEvent = ""
	scanLines(tail, func(line []byte) {
		if !bytes.HasPrefix(line, []byte("data: ")) &&
			!bytes.HasPrefix(line, []byte("event: ")) {
			return
		}
		if bytes.HasPrefix(line, []byte("event: ")) {
			curEvent = string(line[7:])
			return
		}
		data := line[6:]
		if bytes.Equal(data, []byte("[DONE]")) {
			return
		}

		// OpenAI chat / responses API: usage in a data chunk.
		if u.Input == 0 || u.Output == 0 {
			var ck struct {
				Usage *struct {
					PromptTokens     uint32 `json:"prompt_tokens"`
					CompletionTokens uint32 `json:"completion_tokens"`
					InputTokens      uint32 `json:"input_tokens"`
					OutputTokens     uint32 `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(data, &ck) == nil && ck.Usage != nil {
				if u.Input == 0 {
					u.Input = ck.Usage.PromptTokens + ck.Usage.InputTokens
				}
				if u.Output == 0 {
					u.Output = ck.Usage.CompletionTokens + ck.Usage.OutputTokens
				}
			}
		}

		// Anthropic message_delta: output tokens.
		if curEvent == "message_delta" && u.Output == 0 {
			var delta struct {
				Usage struct {
					OutputTokens uint32 `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(data, &delta) == nil {
				u.Output = delta.Usage.OutputTokens
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
