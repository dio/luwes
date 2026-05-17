package sdk_test

import (
	"testing"

	sdk "github.com/dio/luwes"
	"github.com/dio/luwes/shared"
)

// stubFactory is a minimal shared.HttpFilterConfigFactory for registry tests.
type stubFactory struct{ name string }

func (s *stubFactory) Create(_ shared.HttpFilterConfigHandle, _ []byte) (shared.HttpFilterFactory, error) {
	return &stubHttpFactory{}, nil
}
func (s *stubFactory) CreatePerRoute(_ []byte) (any, error) { return nil, nil }

type stubHttpFactory struct{}

func (f *stubHttpFactory) Create(_ shared.HttpFilterHandle) shared.HttpFilter {
	return &shared.EmptyHttpFilter{}
}
func (f *stubHttpFactory) OnDestroy() {}

func TestRegisterRaw_Appears_In_Factories(t *testing.T) {
	// Use a unique name per test to avoid cross-test registry pollution.
	const name = "test-stub-raw"
	sdk.RegisterRaw(name, &stubFactory{name: name})

	factories := sdk.Factories()
	if _, ok := factories[name]; !ok {
		t.Fatalf("factory %q not found after RegisterRaw", name)
	}
}

func TestRegister_Appears_In_Factories(t *testing.T) {
	const name = "test-stub-fn"
	sdk.Register(name, func(h shared.HttpFilterConfigHandle, _ []byte) (shared.HttpFilterFactory, error) {
		return &stubHttpFactory{}, nil
	})

	factories := sdk.Factories()
	if _, ok := factories[name]; !ok {
		t.Fatalf("factory %q not found after Register", name)
	}
}

func TestRegisterSimple_Appears_In_Factories(t *testing.T) {
	const name = "test-stub-simple"
	sdk.RegisterSimple(name, func() shared.HttpFilterFactory {
		return &stubHttpFactory{}
	})

	factories := sdk.Factories()
	if _, ok := factories[name]; !ok {
		t.Fatalf("factory %q not found after RegisterSimple", name)
	}
}

func TestRegisterRaw_Duplicate_Panics(t *testing.T) {
	const name = "test-stub-dup"
	sdk.RegisterRaw(name, &stubFactory{})

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	sdk.RegisterRaw(name, &stubFactory{})
}

func TestFactories_ReturnsCopy(t *testing.T) {
	const name = "test-stub-copy"
	sdk.RegisterRaw(name, &stubFactory{})

	a := sdk.Factories()
	b := sdk.Factories()
	// Mutating one should not affect the other.
	delete(a, name)
	if _, ok := b[name]; !ok {
		t.Fatal("Factories() should return an independent copy each time")
	}
}
