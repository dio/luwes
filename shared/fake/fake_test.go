package fake_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dio/luwes/shared/fake"
)

// -- FakeHeaderMap --

func TestFakeHeaderMap_GetOne(t *testing.T) {
	h := fake.NewFakeHeaderMap(map[string]string{"x-api-key": "secret"})
	buf := h.GetOne("x-api-key")
	require.NotNil(t, buf.Ptr)
	assert.Equal(t, "secret", buf.ToUnsafeString())
}

func TestFakeHeaderMap_GetOne_CaseInsensitive(t *testing.T) {
	h := fake.NewFakeHeaderMap(map[string]string{"Content-Type": "application/json"})
	assert.NotNil(t, h.GetOne("content-type").Ptr, "lowercase lookup")
	assert.NotNil(t, h.GetOne("CONTENT-TYPE").Ptr, "uppercase lookup")
}

func TestFakeHeaderMap_GetOne_Miss(t *testing.T) {
	h := fake.NewFakeHeaderMap(map[string]string{})
	assert.Nil(t, h.GetOne("x-missing").Ptr)
}

func TestFakeHeaderMap_Get_MultiValue(t *testing.T) {
	h := fake.NewFakeHeaderMapMulti(map[string][]string{
		"x-tag": {"a", "b", "c"},
	})
	vals := h.Get("x-tag")
	require.Len(t, vals, 3)
	assert.Equal(t, "a", vals[0].ToUnsafeString())
	assert.Equal(t, "b", vals[1].ToUnsafeString())
	assert.Equal(t, "c", vals[2].ToUnsafeString())
}

func TestFakeHeaderMap_Get_Miss(t *testing.T) {
	h := fake.NewFakeHeaderMap(map[string]string{})
	assert.Nil(t, h.Get("x-missing"))
}

func TestFakeHeaderMap_GetAll(t *testing.T) {
	h := fake.NewFakeHeaderMap(map[string]string{"a": "1", "b": "2"})
	assert.Len(t, h.GetAll(), 2)
}

func TestFakeHeaderMap_Set_RecordsMutation(t *testing.T) {
	h := fake.NewFakeHeaderMap(map[string]string{})
	h.Set("x-foo", "bar")

	assert.Equal(t, "bar", h.GetString("x-foo"))
	require.Len(t, h.Sets, 1)
	assert.Equal(t, fake.SetCall{Key: "x-foo", Value: "bar"}, h.Sets[0])
}

func TestFakeHeaderMap_Set_Overwrites(t *testing.T) {
	h := fake.NewFakeHeaderMap(map[string]string{"x-foo": "old"})
	h.Set("x-foo", "new")
	assert.Equal(t, "new", h.GetString("x-foo"))
}

func TestFakeHeaderMap_Add_RecordsMutation(t *testing.T) {
	h := fake.NewFakeHeaderMap(map[string]string{"x-tag": "a"})
	h.Add("x-tag", "b")

	assert.Len(t, h.Get("x-tag"), 2)
	require.Len(t, h.Adds, 1)
	assert.Equal(t, fake.AddCall{Key: "x-tag", Value: "b"}, h.Adds[0])
}

func TestFakeHeaderMap_Remove(t *testing.T) {
	h := fake.NewFakeHeaderMap(map[string]string{"x-del": "v"})
	h.Remove("x-del")

	assert.Nil(t, h.GetOne("x-del").Ptr)
	require.Len(t, h.Removes, 1)
	assert.Equal(t, "x-del", h.Removes[0])
}

// -- FakeBodyBuffer --

func TestFakeBodyBuffer_GetChunks_Empty(t *testing.T) {
	b := fake.NewFakeBodyBuffer(nil)
	assert.Nil(t, b.GetChunks())
}

func TestFakeBodyBuffer_GetChunks(t *testing.T) {
	b := fake.NewFakeBodyBuffer([]byte("hello"))
	chunks := b.GetChunks()
	require.Len(t, chunks, 1)
	assert.Equal(t, "hello", string(chunks[0].ToUnsafeBytes()))
}

func TestFakeBodyBuffer_GetSize(t *testing.T) {
	b := fake.NewFakeBodyBuffer([]byte("hello"))
	assert.Equal(t, uint64(5), b.GetSize())
}

func TestFakeBodyBuffer_Drain_Partial(t *testing.T) {
	b := fake.NewFakeBodyBuffer([]byte("hello"))
	b.Drain(3)
	assert.Equal(t, "lo", string(b.Body))
}

func TestFakeBodyBuffer_Drain_All(t *testing.T) {
	b := fake.NewFakeBodyBuffer([]byte("hello"))
	b.Drain(10)
	assert.Empty(t, b.Body)
}

func TestFakeBodyBuffer_Append(t *testing.T) {
	b := fake.NewFakeBodyBuffer([]byte("hello"))
	b.Append([]byte(" world"))
	assert.Equal(t, "hello world", string(b.Body))
}
