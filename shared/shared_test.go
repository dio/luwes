package shared_test

import (
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dio/luwes/shared"
)

// -- UnsafeEnvoyBuffer --

func makeBuffer(s string) shared.UnsafeEnvoyBuffer {
	if s == "" {
		return shared.UnsafeEnvoyBuffer{}
	}
	b := []byte(s)
	return shared.UnsafeEnvoyBuffer{Ptr: &b[0], Len: uint64(len(b))}
}

func TestUnsafeEnvoyBuffer_ToUnsafeBytes_Populated(t *testing.T) {
	buf := makeBuffer("hello")
	b := buf.ToUnsafeBytes()
	require.NotNil(t, b)
	assert.Equal(t, []byte("hello"), b)
}

func TestUnsafeEnvoyBuffer_ToUnsafeBytes_Nil(t *testing.T) {
	var buf shared.UnsafeEnvoyBuffer
	assert.Nil(t, buf.ToUnsafeBytes())
}

func TestUnsafeEnvoyBuffer_ToUnsafeBytes_ZeroLen(t *testing.T) {
	data := byte('x')
	buf := shared.UnsafeEnvoyBuffer{Ptr: &data, Len: 0}
	assert.Nil(t, buf.ToUnsafeBytes())
}

func TestUnsafeEnvoyBuffer_ToBytes_Populated(t *testing.T) {
	buf := makeBuffer("world")
	b := buf.ToBytes()
	require.NotNil(t, b)
	assert.Equal(t, []byte("world"), b)
	// Must be a copy: mutating b must not affect the original.
	b[0] = 'X'
	assert.Equal(t, "world", buf.ToUnsafeString())
}

func TestUnsafeEnvoyBuffer_ToBytes_Nil(t *testing.T) {
	var buf shared.UnsafeEnvoyBuffer
	assert.Nil(t, buf.ToBytes())
}

func TestUnsafeEnvoyBuffer_ToUnsafeString_Populated(t *testing.T) {
	buf := makeBuffer("envoy")
	assert.Equal(t, "envoy", buf.ToUnsafeString())
}

func TestUnsafeEnvoyBuffer_ToUnsafeString_Nil(t *testing.T) {
	var buf shared.UnsafeEnvoyBuffer
	assert.Equal(t, "", buf.ToUnsafeString())
}

func TestUnsafeEnvoyBuffer_ToString_Populated(t *testing.T) {
	buf := makeBuffer("copy")
	s := buf.ToString()
	assert.Equal(t, "copy", s)
	// ToString returns a Go-owned copy: verify by checking it's not the same
	// memory as the unsafe string.
	us := buf.ToUnsafeString()
	uptr := (*[2]uintptr)(unsafe.Pointer(&us))[0]
	sptr := (*[2]uintptr)(unsafe.Pointer(&s))[0]
	assert.NotEqual(t, uptr, sptr, "ToString must return an independent copy")
}

func TestUnsafeEnvoyBuffer_ToString_Nil(t *testing.T) {
	var buf shared.UnsafeEnvoyBuffer
	assert.Equal(t, "", buf.ToString())
}

// -- EmptyHttpFilter: confirms default return values --

func TestEmptyHttpFilter_DefaultReturns(t *testing.T) {
	var f shared.EmptyHttpFilter

	assert.Equal(t, shared.HeadersStatusDefault, f.OnRequestHeaders(nil, false))
	assert.Equal(t, shared.BodyStatusDefault, f.OnRequestBody(nil, false))
	assert.Equal(t, shared.TrailersStatusDefault, f.OnRequestTrailers(nil))
	assert.Equal(t, shared.HeadersStatusDefault, f.OnResponseHeaders(nil, false))
	assert.Equal(t, shared.BodyStatusDefault, f.OnResponseBody(nil, false))
	assert.Equal(t, shared.TrailersStatusDefault, f.OnResponseTrailers(nil))
	f.OnStreamComplete() // no-op, no panic
	f.OnDestroy()        // no-op, no panic
	assert.Equal(t, shared.LocalReplyStatusDefault, f.OnLocalReply(200, shared.UnsafeEnvoyBuffer{}, false))
}

// -- EmptyHttpFilterFactory --

func TestEmptyHttpFilterFactory_Create(t *testing.T) {
	var f shared.EmptyHttpFilterFactory
	filter := f.Create(nil)
	assert.NotNil(t, filter)
	// Must satisfy HttpFilter interface.
	_ = filter
	f.OnDestroy() // no-op
}

// -- EmptyHttpFilterConfigFactory --

func TestEmptyHttpFilterConfigFactory_Create(t *testing.T) {
	var f shared.EmptyHttpFilterConfigFactory
	factory, err := f.Create(nil, nil)
	require.NoError(t, err)
	assert.NotNil(t, factory)
}

func TestEmptyHttpFilterConfigFactory_CreatePerRoute(t *testing.T) {
	var f shared.EmptyHttpFilterConfigFactory
	v, err := f.CreatePerRoute(nil)
	assert.NoError(t, err)
	assert.Nil(t, v)
}

// -- EmptyNetworkFilter: confirms default return values --

func TestEmptyNetworkFilter_DefaultReturns(t *testing.T) {
	var f shared.EmptyNetworkFilter

	assert.Equal(t, shared.NetworkFilterStatusDefault, f.OnNewConnection())
	assert.Equal(t, shared.NetworkFilterStatusDefault, f.OnRead(nil, false))
	assert.Equal(t, shared.NetworkFilterStatusDefault, f.OnWrite(nil, false))
	f.OnEvent(0)                        // no-op, no panic
	f.OnDestroy()                       // no-op, no panic
	f.OnAboveWriteBufferHighWatermark() // no-op, no panic
	f.OnBelowWriteBufferLowWatermark()  // no-op, no panic
}

// -- EmptyNetworkFilterFactory --

func TestEmptyNetworkFilterFactory_Create(t *testing.T) {
	var f shared.EmptyNetworkFilterFactory
	filter := f.Create(nil)
	assert.NotNil(t, filter)
	// Must satisfy NetworkFilter interface.
	_ = filter
	f.OnDestroy()
}

// -- EmptyNetworkFilterConfigFactory --

func TestEmptyNetworkFilterConfigFactory_Create(t *testing.T) {
	var f shared.EmptyNetworkFilterConfigFactory
	factory, err := f.Create(nil, nil)
	require.NoError(t, err)
	assert.NotNil(t, factory)
}

// -- ResponseFlagsString --

func TestResponseFlagsString_Zero(t *testing.T) {
	assert.Equal(t, "", shared.ResponseFlagsString(0))
}

func TestResponseFlagsString_SingleFlag(t *testing.T) {
	// bit 5 = UF (UpstreamConnectionFailure)
	assert.Equal(t, "UF", shared.ResponseFlagsString(1<<5))
}

func TestResponseFlagsString_MultipleFlags(t *testing.T) {
	// UT (bit 2) + DC (bit 14)
	got := shared.ResponseFlagsString((1 << 2) | (1 << 14))
	assert.Equal(t, "UT,DC", got)
}

func TestResponseFlagsString_UnknownBit(t *testing.T) {
	// bit 63: beyond known range, rendered as hex
	got := shared.ResponseFlagsString(1 << 63)
	assert.Contains(t, got, "0x")
}

func TestResponseFlagsString_AllKnownFlags(t *testing.T) {
	// All 26 known bits set; result must contain all short names
	var mask uint64
	for i := 0; i < 26; i++ {
		mask |= 1 << uint(i)
	}
	got := shared.ResponseFlagsString(mask)
	for _, name := range []string{"LH", "UH", "UT", "UF", "UC", "DC", "NR", "OM"} {
		assert.Contains(t, got, name, "expected flag %s in %q", name, got)
	}
}

// -- compile-time interface checks --

var (
	_ shared.HttpFilter              = (*shared.EmptyHttpFilter)(nil)
	_ shared.HttpFilterFactory       = (*shared.EmptyHttpFilterFactory)(nil)
	_ shared.HttpFilterConfigFactory = (*shared.EmptyHttpFilterConfigFactory)(nil)
	_ shared.NetworkFilter           = (*shared.EmptyNetworkFilter)(nil)
	_ shared.NetworkFilterFactory    = (*shared.EmptyNetworkFilterFactory)(nil)
)