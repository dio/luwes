package fake_test

import (
	"testing"

	"github.com/dio/luwes/shared/fake"
)

// -- FakeHeaderMap --

func TestFakeHeaderMap_GetOne(t *testing.T) {
	h := fake.NewFakeHeaderMap(map[string]string{"x-api-key": "secret"})
	buf := h.GetOne("x-api-key")
	if buf.Ptr == nil {
		t.Fatal("expected non-nil ptr")
	}
	if got := buf.ToUnsafeString(); got != "secret" {
		t.Fatalf("got %q, want %q", got, "secret")
	}
}

func TestFakeHeaderMap_GetOne_CaseInsensitive(t *testing.T) {
	h := fake.NewFakeHeaderMap(map[string]string{"Content-Type": "application/json"})
	if h.GetOne("content-type").Ptr == nil {
		t.Fatal("lowercase lookup failed")
	}
	if h.GetOne("CONTENT-TYPE").Ptr == nil {
		t.Fatal("uppercase lookup failed")
	}
}

func TestFakeHeaderMap_GetOne_Miss(t *testing.T) {
	h := fake.NewFakeHeaderMap(map[string]string{})
	if h.GetOne("x-missing").Ptr != nil {
		t.Fatal("expected nil ptr on miss")
	}
}

func TestFakeHeaderMap_Get_MultiValue(t *testing.T) {
	h := fake.NewFakeHeaderMapMulti(map[string][]string{
		"x-tag": {"a", "b", "c"},
	})
	vals := h.Get("x-tag")
	if len(vals) != 3 {
		t.Fatalf("got %d values, want 3", len(vals))
	}
	for i, want := range []string{"a", "b", "c"} {
		if got := vals[i].ToUnsafeString(); got != want {
			t.Errorf("vals[%d] = %q, want %q", i, got, want)
		}
	}
}

func TestFakeHeaderMap_Get_Miss(t *testing.T) {
	h := fake.NewFakeHeaderMap(map[string]string{})
	if vals := h.Get("x-missing"); vals != nil {
		t.Fatalf("expected nil on miss, got %v", vals)
	}
}

func TestFakeHeaderMap_GetAll(t *testing.T) {
	h := fake.NewFakeHeaderMap(map[string]string{"a": "1", "b": "2"})
	if len(h.GetAll()) != 2 {
		t.Fatalf("got %d pairs, want 2", len(h.GetAll()))
	}
}

func TestFakeHeaderMap_Set_RecordsMutation(t *testing.T) {
	h := fake.NewFakeHeaderMap(map[string]string{})
	h.Set("x-foo", "bar")

	if got := h.GetString("x-foo"); got != "bar" {
		t.Fatalf("got %q, want %q", got, "bar")
	}
	if len(h.Sets) != 1 || h.Sets[0].Key != "x-foo" || h.Sets[0].Value != "bar" {
		t.Fatalf("mutation not recorded: %+v", h.Sets)
	}
}

func TestFakeHeaderMap_Set_Overwrites(t *testing.T) {
	h := fake.NewFakeHeaderMap(map[string]string{"x-foo": "old"})
	h.Set("x-foo", "new")
	if got := h.GetString("x-foo"); got != "new" {
		t.Fatalf("got %q, want %q", got, "new")
	}
}

func TestFakeHeaderMap_Add_RecordsMutation(t *testing.T) {
	h := fake.NewFakeHeaderMap(map[string]string{"x-tag": "a"})
	h.Add("x-tag", "b")

	vals := h.Get("x-tag")
	if len(vals) != 2 {
		t.Fatalf("got %d values after Add, want 2", len(vals))
	}
	if len(h.Adds) != 1 || h.Adds[0].Key != "x-tag" || h.Adds[0].Value != "b" {
		t.Fatalf("mutation not recorded: %+v", h.Adds)
	}
}

func TestFakeHeaderMap_Remove(t *testing.T) {
	h := fake.NewFakeHeaderMap(map[string]string{"x-del": "v"})
	h.Remove("x-del")

	if h.GetOne("x-del").Ptr != nil {
		t.Fatal("header should be gone after Remove")
	}
	if len(h.Removes) != 1 || h.Removes[0] != "x-del" {
		t.Fatalf("mutation not recorded: %+v", h.Removes)
	}
}

// -- FakeBodyBuffer --

func TestFakeBodyBuffer_GetChunks_Empty(t *testing.T) {
	b := fake.NewFakeBodyBuffer(nil)
	if chunks := b.GetChunks(); chunks != nil {
		t.Fatalf("expected nil chunks for empty body, got %v", chunks)
	}
}

func TestFakeBodyBuffer_GetChunks(t *testing.T) {
	b := fake.NewFakeBodyBuffer([]byte("hello"))
	chunks := b.GetChunks()
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if got := string(chunks[0].ToUnsafeBytes()); got != "hello" {
		t.Fatalf("got %q, want %q", got, "hello")
	}
}

func TestFakeBodyBuffer_GetSize(t *testing.T) {
	b := fake.NewFakeBodyBuffer([]byte("hello"))
	if got := b.GetSize(); got != 5 {
		t.Fatalf("got %d, want 5", got)
	}
}

func TestFakeBodyBuffer_Drain_Partial(t *testing.T) {
	b := fake.NewFakeBodyBuffer([]byte("hello"))
	b.Drain(3)
	if got := string(b.Body); got != "lo" {
		t.Fatalf("after Drain(3): got %q, want %q", got, "lo")
	}
}

func TestFakeBodyBuffer_Drain_All(t *testing.T) {
	b := fake.NewFakeBodyBuffer([]byte("hello"))
	b.Drain(10)
	if len(b.Body) != 0 {
		t.Fatalf("expected empty body after over-drain, got %q", b.Body)
	}
}

func TestFakeBodyBuffer_Append(t *testing.T) {
	b := fake.NewFakeBodyBuffer([]byte("hello"))
	b.Append([]byte(" world"))
	if got := string(b.Body); got != "hello world" {
		t.Fatalf("got %q, want %q", got, "hello world")
	}
}
