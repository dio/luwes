package ssetap_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dio/luwes/buffer"
	"github.com/dio/luwes/sahl"
	ssetap "github.com/dio/luwes/sahl/examples/sse-tap"
	sahltest "github.com/dio/luwes/sahl/testutil"
	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// feedStream feeds chunks into a HeadTail ring and returns extracted usage.
// Mirrors the real filter path without any Envoy involvement.
func feedStream(chunks []string) ssetap.TokenUsage {
	ht := buffer.NewHeadTail(8*1024, 64*1024)
	for _, c := range chunks {
		ht.Write([]byte(c))
	}
	return ssetap.ExtractUsage(ht.Head(), ht.Tail())
}

func TestExtractUsage_OpenAI_Chat(t *testing.T) {
	sse := strings.Join([]string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\" there\"}}]}\n\n",
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\n",
		"data: [DONE]\n\n",
	}, "")

	u := feedStream([]string{sse})
	assert.Equal(t, uint32(10), u.Input)
	assert.Equal(t, uint32(5), u.Output)
}

func TestExtractUsage_OpenAI_ResponsesAPI(t *testing.T) {
	sse := strings.Join([]string{
		"event: response.created\ndata: {}\n\n",
		"event: response.completed\ndata: {\"usage\":{\"input_tokens\":20,\"output_tokens\":8}}\n\n",
	}, "")

	u := feedStream([]string{sse})
	assert.Equal(t, uint32(20), u.Input)
	assert.Equal(t, uint32(8), u.Output)
}

func TestExtractUsage_Anthropic_Messages(t *testing.T) {
	sse := strings.Join([]string{
		"event: message_start\n",
		"data: {\"message\":{\"usage\":{\"input_tokens\":42}}}\n\n",
		"event: content_block_delta\ndata: {\"delta\":{\"text\":\"Hi\"}}\n\n",
		"event: content_block_delta\ndata: {\"delta\":{\"text\":\" there\"}}\n\n",
		"event: message_delta\n",
		"data: {\"usage\":{\"output_tokens\":15}}\n\n",
		"event: message_stop\ndata: {}\n\n",
	}, "")

	u := feedStream([]string{sse})
	assert.Equal(t, uint32(42), u.Input)
	assert.Equal(t, uint32(15), u.Output)
}

func TestExtractUsage_LargeStream_HeadTailOnly(t *testing.T) {
	// Simulate a large stream: only head (first 8 KB) and tail (last 64 KB) are scanned.
	head := "event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":99}}}\n\n"
	middle := strings.Repeat("data: {\"delta\":{\"text\":\"x\"}}\n\n", 5000) // ~200 KB filler
	tail := "event: message_delta\ndata: {\"usage\":{\"output_tokens\":77}}\n\n"

	ht := buffer.NewHeadTail(8*1024, 64*1024)
	ht.Write([]byte(head))
	ht.Write([]byte(middle))
	ht.Write([]byte(tail))

	u := ssetap.ExtractUsage(ht.Head(), ht.Tail())
	assert.Equal(t, uint32(99), u.Input)
	assert.Equal(t, uint32(77), u.Output)
}

func TestExtractUsage_ChunkedDelivery(t *testing.T) {
	// Envoy may deliver SSE in small chunks. Verify ring handles split lines.
	full := "event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":7}}}\n\n" +
		"event: message_delta\ndata: {\"usage\":{\"output_tokens\":3}}\n\n"

	var chunks []string
	for i := 0; i < len(full); i += 10 {
		end := i + 10
		if end > len(full) {
			end = len(full)
		}
		chunks = append(chunks, full[i:end])
	}

	u := feedStream(chunks)
	assert.Equal(t, uint32(7), u.Input)
	assert.Equal(t, uint32(3), u.Output)
}

func TestExtractUsage_NonSSE_ReturnsZero(t *testing.T) {
	// Plain JSON: no SSE prefix, extraction returns zero.
	body := `{"choices":[{"message":{"content":"Hi"}}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`
	u := feedStream([]string{body})
	assert.Equal(t, uint32(0), u.Input)
	assert.Equal(t, uint32(0), u.Output)
}

func TestExtractUsage_EmptyStream(t *testing.T) {
	u := feedStream([]string{})
	assert.Equal(t, uint32(0), u.Input)
	assert.Equal(t, uint32(0), u.Output)
}

// -- tapResponse integration tests --
// Exercise the full response observer path: ringState lifecycle, skip flag,
// multi-chunk accumulation, counter emission, and no-token early return.

func makeTapFilter(fh *fake.FakeFilterHandle) *sahltest.Filter {
	return sahltest.NewFilterWithResponse(
		ssetap.ExtensionName,
		func(w *sahl.Writer, r *sahl.Request) {},
		ssetap.TapResponseForTest,
		fh,
	)
}

func TestTapResponse_SSE_ExtractsTokens(t *testing.T) {
	sseBody := "event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":12}}}\n\n" +
		"event: message_delta\ndata: {\"usage\":{\"output_tokens\":6}}\n\n"

	fh := fake.NewFilterHandle(
		fake.WithResponseHeaders(map[string]string{
			":status":      "200",
			"content-type": "text/event-stream",
		}),
		fake.WithResponseBody([]byte(sseBody)),
	)

	f := makeTapFilter(fh)
	f.OnRequestHeaders(fh.RequestHeaders(), false)
	f.OnResponseHeaders(fh.ResponseHeaders(), false)
	f.OnResponseBody(fh.BufferedResponseBody(), true)
	f.OnStreamComplete()

	require.Len(t, fh.CounterIncrements, 2)
	var totalN uint64
	for _, ci := range fh.CounterIncrements {
		totalN += ci.N
	}
	assert.Equal(t, uint64(12+6), totalN)
}

func TestTapResponse_NonSSE_Skipped(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithResponseHeaders(map[string]string{
			":status":      "200",
			"content-type": "application/json",
		}),
		fake.WithResponseBody([]byte(`{"result":"ok"}`)),
	)

	f := makeTapFilter(fh)
	f.OnRequestHeaders(fh.RequestHeaders(), false)
	f.OnResponseHeaders(fh.ResponseHeaders(), false)
	f.OnResponseBody(fh.BufferedResponseBody(), true)
	f.OnStreamComplete()

	assert.Empty(t, fh.CounterIncrements)
}

func TestTapResponse_MultiChunk(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithResponseHeaders(map[string]string{
			":status":      "200",
			"content-type": "text/event-stream",
		}),
	)

	f := makeTapFilter(fh)
	f.OnRequestHeaders(fh.RequestHeaders(), false)
	f.OnResponseHeaders(fh.ResponseHeaders(), false)
	chunk1 := "event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":3}}}\n\n"
	chunk2 := "event: message_delta\ndata: {\"usage\":{\"output_tokens\":7}}\n\n"
	f.OnResponseBody(fake.NewFakeBodyBuffer([]byte(chunk1)), false)
	f.OnResponseBody(fake.NewFakeBodyBuffer([]byte(chunk2)), true)
	f.OnStreamComplete()

	require.Len(t, fh.CounterIncrements, 2)
}

func TestTapResponse_NoTokensNoCounters(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithResponseHeaders(map[string]string{
			":status":      "200",
			"content-type": "text/event-stream",
		}),
		fake.WithResponseBody([]byte("data: {\"delta\":\"hi\"}\n\n")),
	)

	f := makeTapFilter(fh)
	f.OnRequestHeaders(fh.RequestHeaders(), false)
	f.OnResponseHeaders(fh.ResponseHeaders(), false)
	f.OnResponseBody(fh.BufferedResponseBody(), true)
	f.OnStreamComplete()

	assert.Empty(t, fh.CounterIncrements)
}

// BenchmarkExtractUsage measures scan cost on a realistic 64 KB tail buffer.
func BenchmarkExtractUsage(b *testing.B) {
	tail := []byte(strings.Repeat("data: {\"choices\":[{\"delta\":{\"text\":\"x\"}}]}\n\n", 1000) +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":50}}\n\n" +
		"data: [DONE]\n\n")
	head := []byte("data: {}\n\n")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ssetap.ExtractUsage(head, tail)
	}
}

func ExampleExtractUsage() {
	ht := buffer.NewHeadTail(8*1024, 64*1024)
	ht.Write([]byte("event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n"))
	ht.Write([]byte("event: message_delta\ndata: {\"usage\":{\"output_tokens\":5}}\n\n"))

	u := ssetap.ExtractUsage(ht.Head(), ht.Tail())
	fmt.Printf("input=%d output=%d\n", u.Input, u.Output)
	// Output: input=10 output=5
}

// unused imports needed via the sahltest package
var _ = shared.LogLevelInfo
