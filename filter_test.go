package sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdk "github.com/dio/luwes"
	"github.com/dio/luwes/shared"
)

type stubFactory struct{}

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
	const name = "test-stub-raw"
	sdk.RegisterRaw(name, &stubFactory{})
	assert.Contains(t, sdk.Factories(), name)
}

func TestRegister_Appears_In_Factories(t *testing.T) {
	const name = "test-stub-fn"
	sdk.Register(name, func(_ shared.HttpFilterConfigHandle, _ []byte) (shared.HttpFilterFactory, error) {
		return &stubHttpFactory{}, nil
	})
	assert.Contains(t, sdk.Factories(), name)
}

func TestRegisterSimple_Appears_In_Factories(t *testing.T) {
	const name = "test-stub-simple"
	sdk.RegisterSimple(name, func() shared.HttpFilterFactory { return &stubHttpFactory{} })
	assert.Contains(t, sdk.Factories(), name)
}

func TestRegisterRaw_Duplicate_Panics(t *testing.T) {
	const name = "test-stub-dup"
	sdk.RegisterRaw(name, &stubFactory{})
	assert.Panics(t, func() { sdk.RegisterRaw(name, &stubFactory{}) })
}

func TestFactories_ReturnsCopy(t *testing.T) {
	const name = "test-stub-copy"
	sdk.RegisterRaw(name, &stubFactory{})

	a := sdk.Factories()
	delete(a, name)
	require.Contains(t, sdk.Factories(), name, "Factories() must return an independent copy")
}

// ---- Access logger registration ----

type stubAccessLoggerHttpFactory struct{}

func (f *stubAccessLoggerHttpFactory) NewLogger() shared.AccessLogger {
	return &shared.EmptyAccessLogger{}
}
func (f *stubAccessLoggerHttpFactory) OnDestroy() {}

func TestRegisterAccessLogger_Appears_In_AccessLoggerFactories(t *testing.T) {
	const name = "test-access-logger-fn"
	sdk.RegisterAccessLogger(name, func(_ shared.AccessLoggerConfigHandle, _ []byte) (shared.AccessLoggerFactory, error) {
		return &stubAccessLoggerHttpFactory{}, nil
	})
	assert.Contains(t, sdk.AccessLoggerFactories(), name)
}

func TestRegisterAccessLogger_Duplicate_Panics(t *testing.T) {
	const name = "test-access-logger-dup"
	sdk.RegisterAccessLogger(name, func(_ shared.AccessLoggerConfigHandle, _ []byte) (shared.AccessLoggerFactory, error) {
		return &stubAccessLoggerHttpFactory{}, nil
	})
	assert.Panics(t, func() {
		sdk.RegisterAccessLogger(name, func(_ shared.AccessLoggerConfigHandle, _ []byte) (shared.AccessLoggerFactory, error) {
			return &stubAccessLoggerHttpFactory{}, nil
		})
	})
}

func TestAccessLoggerFactories_ReturnsCopy(t *testing.T) {
	const name = "test-access-logger-copy"
	sdk.RegisterAccessLogger(name, func(_ shared.AccessLoggerConfigHandle, _ []byte) (shared.AccessLoggerFactory, error) {
		return &stubAccessLoggerHttpFactory{}, nil
	})
	a := sdk.AccessLoggerFactories()
	delete(a, name)
	require.Contains(t, sdk.AccessLoggerFactories(), name, "AccessLoggerFactories() must return an independent copy")
}
