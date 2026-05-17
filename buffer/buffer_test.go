package buffer_test

import (
	"testing"
	"unsafe"

	"github.com/dio/luwes/buffer"
	"github.com/dio/luwes/shared"
	"github.com/stretchr/testify/assert"
)

// makeChunks converts string slices to UnsafeEnvoyBuffer slices for WriteChunks tests.
func makeChunks(strs ...string) []shared.UnsafeEnvoyBuffer {
	out := make([]shared.UnsafeEnvoyBuffer, len(strs))
	for i, s := range strs {
		out[i] = shared.UnsafeEnvoyBuffer{
			Ptr: (*byte)(unsafe.Pointer(unsafe.StringData(s))),
			Len: uint64(len(s)),
		}
	}
	return out
}

// -- Ring --

func TestRing_BasicWrite(t *testing.T) {
	rb := buffer.NewRing(8)
	rb.Write([]byte("hello"))
	assert.Equal(t, "hello", string(rb.Bytes()))
}

func TestRing_ExactlyFull(t *testing.T) {
	rb := buffer.NewRing(5)
	rb.Write([]byte("hello"))
	assert.Equal(t, "hello", string(rb.Bytes()))
}

func TestRing_Overflow_KeepsLast(t *testing.T) {
	rb := buffer.NewRing(5)
	rb.Write([]byte("hello world")) // 11 bytes into 5-byte ring: last 5 = "world"
	assert.Equal(t, "world", string(rb.Bytes()))
}

func TestRing_MultipleChunks(t *testing.T) {
	rb := buffer.NewRing(10)
	rb.Write([]byte("hello "))
	rb.Write([]byte("world")) // total 11 into 10: last 10 = "ello world"
	assert.Equal(t, "ello world", string(rb.Bytes()))
}

func TestRing_Reset(t *testing.T) {
	rb := buffer.NewRing(8)
	rb.Write([]byte("hello"))
	rb.Reset()
	assert.Equal(t, "", string(rb.Bytes()))
	assert.Equal(t, 0, rb.Len())
}

func TestRing_Len(t *testing.T) {
	rb := buffer.NewRing(8)
	assert.Equal(t, 0, rb.Len())
	rb.Write([]byte("hi"))
	assert.Equal(t, 2, rb.Len())
	rb.Write([]byte("hello world")) // overflow
	assert.Equal(t, 8, rb.Len())
}

func TestRing_WriteChunks(t *testing.T) {
	rb := buffer.NewRing(20)
	rb.WriteChunks(makeChunks("hello ", "world"))
	assert.Equal(t, "hello world", string(rb.Bytes()))
}

func TestRing_WriteChunks_Overflow(t *testing.T) {
	rb := buffer.NewRing(5)
	rb.WriteChunks(makeChunks("hello ", "world")) // 11 bytes into 5: last 5 = "world"
	assert.Equal(t, "world", string(rb.Bytes()))
}

func TestRing_WriteChunks_Empty(t *testing.T) {
	rb := buffer.NewRing(8)
	rb.WriteChunks(nil)
	assert.Equal(t, 0, rb.Len())
	rb.WriteChunks([]shared.UnsafeEnvoyBuffer{})
	assert.Equal(t, 0, rb.Len())
}

// -- HeadTail --

func TestHeadTail_CapturesBothEnds(t *testing.T) {
	ht := buffer.NewHeadTail(4, 4)
	ht.Write([]byte("abcdefghijkl")) // head: "abcd", tail: last 4 = "ijkl"
	assert.Equal(t, "abcd", string(ht.Head()))
	assert.Equal(t, "ijkl", string(ht.Tail()))
}

func TestHeadTail_ShortStream(t *testing.T) {
	ht := buffer.NewHeadTail(8, 8)
	ht.Write([]byte("hi"))
	assert.Equal(t, "hi", string(ht.Head()))
	assert.Equal(t, "hi", string(ht.Tail()))
}

func TestHeadTail_ChunkedWrites(t *testing.T) {
	ht := buffer.NewHeadTail(4, 4)
	ht.Write([]byte("ab"))
	ht.Write([]byte("cd"))
	ht.Write([]byte("ef"))
	ht.Write([]byte("gh"))
	// head: first 4 = "abcd"; tail ring 4: last 4 = "efgh"
	assert.Equal(t, "abcd", string(ht.Head()))
	assert.Equal(t, "efgh", string(ht.Tail()))
}

func TestHeadTail_Reset(t *testing.T) {
	ht := buffer.NewHeadTail(8, 8)
	ht.Write([]byte("hello world"))
	ht.Reset()
	assert.Empty(t, string(ht.Head()))
	assert.Empty(t, string(ht.Tail()))
}

func TestHeadTail_WriteChunks(t *testing.T) {
	ht := buffer.NewHeadTail(8, 8)
	ht.WriteChunks(makeChunks("hello ", "world"))
	// "hello world" = 11 bytes.
	// head slab (8): captures first 8 = "hello wo"
	// tail ring (8): last 8 = "lo world"
	assert.Equal(t, "hello wo", string(ht.Head()))
	assert.Equal(t, "lo world", string(ht.Tail()))
}

func TestHeadTail_WriteChunks_LargeStream(t *testing.T) {
	ht := buffer.NewHeadTail(4, 4)
	// Three chunks: head captures first 4 bytes, tail captures last 4
	ht.WriteChunks(makeChunks("abcd", "efgh", "ijkl"))
	assert.Equal(t, "abcd", string(ht.Head()))
	assert.Equal(t, "ijkl", string(ht.Tail()))
}

func TestHeadTail_WriteChunks_Empty(t *testing.T) {
	ht := buffer.NewHeadTail(8, 8)
	ht.WriteChunks(nil)
	assert.Empty(t, string(ht.Head()))
	ht.WriteChunks([]shared.UnsafeEnvoyBuffer{})
	assert.Empty(t, string(ht.Head()))
}

func TestHeadTail_WriteChunks_MatchesWriteByte(t *testing.T) {
	// WriteChunks must produce the same result as equivalent Write calls.
	ht1 := buffer.NewHeadTail(8, 16)
	ht1.Write([]byte("foo"))
	ht1.Write([]byte("bar"))
	ht1.Write([]byte("baz"))

	ht2 := buffer.NewHeadTail(8, 16)
	ht2.WriteChunks(makeChunks("foo", "bar", "baz"))

	assert.Equal(t, string(ht1.Head()), string(ht2.Head()))
	assert.Equal(t, string(ht1.Tail()), string(ht2.Tail()))
}
