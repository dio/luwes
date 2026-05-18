package llmproxy

import (
	"bytes"
	"encoding/json"
)

// tokenUsage holds extracted token counts from a streaming LLM response.
type tokenUsage struct {
	Input  uint32
	Output uint32
}

// extractUsage scans head for input tokens and tail for output tokens.
// Handles both OpenAI and Anthropic SSE wire formats.
// Identical logic to sahl/examples/sse-tap ExtractUsage, inlined here
// to avoid a sahl dependency in the raw luwes example.
func extractUsage(head, tail []byte) tokenUsage {
	var u tokenUsage

	// Scan head: Anthropic message_start carries input tokens.
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

	// Scan tail: OpenAI and Anthropic output tokens.
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

		// OpenAI: usage in a data chunk.
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
