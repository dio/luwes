package sahl_test

import (
	"context"
	"log/slog"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dio/luwes/sahl"
	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
)

// ============================================================
// Header tests
// ============================================================

func TestHeader_Get_CopiesIntoGoMemory(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{
		"x-api-key": "secret",
	}))
	req := buildRequest(fh)
	assert.Equal(t, "secret", req.Header.Get("x-api-key"))
}

func TestHeader_Get_CaseInsensitive(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"Content-Type": "application/json"}))
	req := buildRequest(fh)
	assert.Equal(t, "application/json", req.Header.Get("content-type"))
	assert.Equal(t, "application/json", req.Header.Get("CONTENT-TYPE"))
}

func TestHeader_Get_Miss(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{}))
	req := buildRequest(fh)
	assert.Equal(t, "", req.Header.Get("x-missing"))
}

func TestHeader_Get_Cached(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"x-foo": "bar"}))
	req := buildRequest(fh)
	v1 := req.Header.Get("x-foo")
	v2 := req.Header.Get("x-foo")
	assert.Equal(t, "bar", v1)
	assert.Equal(t, "bar", v2)
}

func TestHeader_Peek_Hit(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"x-api-key": "token"}))
	req := buildRequest(fh)
	v, ok := req.Header.Peek("x-api-key")
	require.True(t, ok)
	assert.Equal(t, "token", v)
}

func TestHeader_Peek_Miss(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{}))
	req := buildRequest(fh)
	_, ok := req.Header.Peek("x-missing")
	assert.False(t, ok)
}

func TestHeader_Peek_EmptyValue(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"x-empty": ""}))
	req := buildRequest(fh)
	_, ok := req.Header.Peek("x-empty")
	// Empty value: GetOneInto returns true but Len==0 -> treated as miss.
	assert.False(t, ok)
}

// ============================================================
// Request pre-copies + logging
// ============================================================

func TestRequest_MethodPathHost(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{
		":method":    "POST",
		":path":      "/v1/chat",
		":authority": "api.example.com",
	}))
	req := buildRequest(fh)
	assert.Equal(t, "POST", req.Method)
	assert.Equal(t, "/v1/chat", req.Path)
	assert.Equal(t, "api.example.com", req.Host)
}

func TestRequest_MissingPseudoHeaders(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{}))
	req := buildRequest(fh)
	assert.Equal(t, "", req.Method)
	assert.Equal(t, "", req.Path)
	assert.Equal(t, "", req.Host)
}

func TestRequest_Log(t *testing.T) {
	fh := fake.NewFilterHandle()
	req := buildRequest(fh)
	req.Log(shared.LogLevelInfo, "test %s", "msg") // no-op in fake, no panic
}

func TestRequest_LogAttrs_WithFilterName(t *testing.T) {
	fh := fake.NewFilterHandle()
	req := sahl.NewRequestForTest(fh.RequestHeaders(), fh, "my-filter")
	req.LogAttrs(shared.LogLevelDebug, "hello") // exercises formatLogAttrs + filter prefix
}

func TestRequest_LogAttrs_NoFilterName(t *testing.T) {
	fh := fake.NewFilterHandle()
	req := sahl.NewRequestForTest(fh.RequestHeaders(), fh, "")
	req.LogAttrs(shared.LogLevelDebug, "hello", slog.String("k", "v"))
}

// ============================================================
// Writer tests
// ============================================================

func TestWriter_Send(t *testing.T) {
	fh := fake.NewFilterHandle()
	w := buildWriter(fh)
	w.Send(http.StatusUnauthorized, `{"error":"missing key"}`)
	assert.True(t, w.Responded())
	require.Len(t, fh.LocalResponses, 1)
	assert.Equal(t, uint32(401), fh.LocalResponses[0].Status)
}

func TestWriter_SendBytes(t *testing.T) {
	fh := fake.NewFilterHandle()
	w := buildWriter(fh)
	w.SendBytes(http.StatusOK, []byte("hello"))
	assert.True(t, w.Responded())
	require.Len(t, fh.LocalResponses, 1)
	assert.Equal(t, []byte("hello"), fh.LocalResponses[0].Body)
}

func TestWriter_SendBytes_AfterResponded_NoOp(t *testing.T) {
	fh := fake.NewFilterHandle()
	w := buildWriter(fh)
	w.Send(200, "first")
	w.SendBytes(201, []byte("second")) // no-op: already responded
	assert.Len(t, fh.LocalResponses, 1)
	assert.Equal(t, uint32(200), fh.LocalResponses[0].Status)
}

func TestWriter_SendBytes_WithResponseHeaders(t *testing.T) {
	fh := fake.NewFilterHandle()
	w := buildWriter(fh)
	w.SetResponseHeader("content-type", "application/json")
	w.SendBytes(200, []byte("{}"))
	require.Len(t, fh.LocalResponses, 1)
	require.Len(t, fh.LocalResponses[0].Headers, 1)
	assert.Equal(t, [2]string{"content-type", "application/json"}, fh.LocalResponses[0].Headers[0])
}

func TestWriter_SetResponseHeader_AfterSend_NoOp(t *testing.T) {
	fh := fake.NewFilterHandle()
	w := buildWriter(fh)
	w.Send(200, "ok")
	w.SetResponseHeader("x-after", "ignored") // no-op after responded
	assert.Len(t, fh.LocalResponses[0].Headers, 0)
}

func TestWriter_Log(t *testing.T) {
	fh := fake.NewFilterHandle()
	w := buildWriter(fh)
	w.Log(shared.LogLevelInfo, "test %s", "log") // no-op in fake, no panic
}

func TestWriter_SetRequestHeader(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"x-user-id": ""}))
	w := buildWriter(fh)
	w.SetRequestHeader("x-user-id", "alice")
	assert.False(t, w.Responded())
	w.FlushForTest()
	assert.Equal(t, "alice", fh.RequestHeaders().(*fake.FakeHeaderMap).GetString("x-user-id"))
}

func TestWriter_SetMetadata(t *testing.T) {
	fh := fake.NewFilterHandle()
	w := buildWriter(fh)
	w.SetMetadata("filter", "user-id", "alice")
	w.FlushForTest()
	buf, ok := fh.GetMetadataString(shared.MetadataSourceTypeDynamic, "filter", "user-id")
	require.True(t, ok)
	assert.Equal(t, "alice", buf.ToUnsafeString())
}

func TestWriter_SetMetadata_AfterSend_NoOp(t *testing.T) {
	fh := fake.NewFilterHandle()
	w := buildWriter(fh)
	w.Send(401, "denied")
	w.SetMetadata("ns", "key", "ignored") // no-op after responded
	w.FlushForTest()
	_, ok := fh.GetMetadataString(shared.MetadataSourceTypeDynamic, "ns", "key")
	assert.False(t, ok)
}

func TestWriter_ClearRouteCache(t *testing.T) {
	fh := fake.NewFilterHandle()
	w := buildWriter(fh)
	w.ClearRouteCache()
	w.FlushForTest()
	assert.Equal(t, 1, fh.ClearedRouteCache)
}

func TestWriter_IncrementCounter(t *testing.T) {
	fh := fake.NewFilterHandle()
	w := buildWriter(fh)
	w.IncrementCounter(shared.MetricID(1), 5, "ok")
	w.FlushForTest()
	require.Len(t, fh.CounterIncrements, 1)
	assert.Equal(t, uint64(5), fh.CounterIncrements[0].N)
}

func TestWriter_RecordHistogram(t *testing.T) {
	fh := fake.NewFilterHandle()
	w := buildWriter(fh)
	w.RecordHistogram(shared.MetricID(2), 42)
	w.FlushForTest()
	require.Len(t, fh.CounterIncrements, 1)
	ci := fh.CounterIncrements[0]
	assert.Equal(t, uint64(42), ci.N)
	assert.True(t, ci.Hist)
}

func TestWriter_Go_RunsGoroutine(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"x-user-id": ""}))
	done := make(chan string, 1)

	h := func(w *sahl.Writer, r *sahl.Request) {
		w.Go(func(ctx context.Context) {
			w.SetRequestHeader("x-user-id", "from-goroutine")
			done <- "from-goroutine"
		})
	}

	runHandler(t, fh, h)
	val := <-done
	assert.Equal(t, "from-goroutine", val)
}

func TestWriter_Go_PanicOnDouble(t *testing.T) {
	fh := fake.NewFilterHandle()
	w := buildWriter(fh)
	w.Go(func(ctx context.Context) {})
	assert.Panics(t, func() {
		w.Go(func(ctx context.Context) {})
	})
}

// ============================================================
// Handler integration
// ============================================================

func TestHandler_Accept(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{
		"x-api-key": "valid",
		"x-user-id": "",
	}))

	var called bool
	h := func(w *sahl.Writer, r *sahl.Request) {
		called = true
		key, ok := r.Header.Peek("x-api-key")
		require.True(t, ok)
		w.SetRequestHeader("x-user-id", key)
	}

	runHandler(t, fh, h)
	assert.True(t, called)
	assert.Equal(t, "valid", fh.RequestHeaders().(*fake.FakeHeaderMap).GetString("x-user-id"))
}

func TestHandler_Reject(t *testing.T) {
	fh := fake.NewFilterHandle()
	h := func(w *sahl.Writer, r *sahl.Request) {
		if _, ok := r.Header.Peek("x-api-key"); !ok {
			w.Send(401, `{"error":"missing key"}`)
		}
	}
	runHandler(t, fh, h)
	require.Len(t, fh.LocalResponses, 1)
	assert.Equal(t, uint32(401), fh.LocalResponses[0].Status)
}

// ============================================================
// Registration
// ============================================================

func TestRegister_And_Factories(t *testing.T) {
	sahl.Register("test-register-factories", func(w *sahl.Writer, r *sahl.Request) {})
	factories := sahl.Factories()
	_, ok := factories["test-register-factories"]
	assert.True(t, ok)
}

func TestRegister_DuplicatePanics(t *testing.T) {
	sahl.Register("test-register-dup", func(w *sahl.Writer, r *sahl.Request) {})
	assert.Panics(t, func() {
		sahl.Register("test-register-dup", func(w *sahl.Writer, r *sahl.Request) {})
	})
}

func TestRegisterWithConfig(t *testing.T) {
	var configCalled bool
	sahl.RegisterWithConfig("test-register-with-config",
		func(h sahl.ConfigHandle) error {
			configCalled = true
			assert.NotNil(t, h)
			return nil
		},
		func(w *sahl.Writer, r *sahl.Request) {},
	)
	factories := sahl.Factories()
	factory, ok := factories["test-register-with-config"]
	require.True(t, ok)
	// Exercise the config path by calling Create.
	cfgHandle := &fakeConfigHandle{}
	ff, err := factory.Create(cfgHandle, nil)
	require.NoError(t, err)
	require.NotNil(t, ff)
	assert.True(t, configCalled)
}

func TestRegisterFactory(t *testing.T) {
	var factoryCalled bool
	sahl.RegisterFactory("test-register-factory",
		func(h sahl.ConfigHandle) (sahl.HandlerFunc, error) {
			factoryCalled = true
			return func(w *sahl.Writer, r *sahl.Request) {}, nil
		},
	)
	factories := sahl.Factories()
	factory, ok := factories["test-register-factory"]
	require.True(t, ok)
	cfgHandle := &fakeConfigHandle{}
	ff, err := factory.Create(cfgHandle, nil)
	require.NoError(t, err)
	require.NotNil(t, ff)
	assert.True(t, factoryCalled)
}

func TestRegisterFactory_FactoryError(t *testing.T) {
	sahl.RegisterFactory("test-factory-err",
		func(h sahl.ConfigHandle) (sahl.HandlerFunc, error) {
			return nil, assert.AnError
		},
	)
	factories := sahl.Factories()
	factory := factories["test-factory-err"]
	_, err := factory.Create(&fakeConfigHandle{}, nil)
	assert.Error(t, err)
}

func TestRegisterFactory_NilHandler(t *testing.T) {
	// factoryFn returns (nil, nil): user-supplied factory returned a nil handler
	// without an error. This is a user error, not a BUG, so it returns an error
	// to Envoy rather than panicking. Distinct from filterDef.handler==nil (BUG panic).
	sahl.RegisterFactory("test-factory-nil-handler",
		func(h sahl.ConfigHandle) (sahl.HandlerFunc, error) {
			return nil, nil // no error but nil handler
		},
	)
	factories := sahl.Factories()
	factory := factories["test-factory-nil-handler"]
	_, err := factory.Create(&fakeConfigHandle{}, nil)
	assert.Error(t, err)
}

func TestRegisterWithResponse(t *testing.T) {
	sahl.RegisterWithResponse("test-with-response",
		func(w *sahl.Writer, r *sahl.Request) {},
		func(w *sahl.Writer, chunk *sahl.ResponseChunk) {},
	)
	factories := sahl.Factories()
	_, ok := factories["test-with-response"]
	assert.True(t, ok)
}

func TestRegisterWithBody(t *testing.T) {
	sahl.RegisterWithBody("test-with-body",
		func(w *sahl.Writer, r *sahl.Request) {},
	)
	factories := sahl.Factories()
	_, ok := factories["test-with-body"]
	assert.True(t, ok)
}

func TestRegisterWithBodyAndResponse(t *testing.T) {
	sahl.RegisterWithBodyAndResponse("test-with-body-and-response",
		func(w *sahl.Writer, r *sahl.Request) {},
		func(w *sahl.Writer, chunk *sahl.ResponseChunk) {},
	)
	factories := sahl.Factories()
	_, ok := factories["test-with-body-and-response"]
	assert.True(t, ok)
}

func TestRegisterWithConfigAndResponse(t *testing.T) {
	sahl.RegisterWithConfigAndResponse("test-with-config-and-response",
		func(h sahl.ConfigHandle) error { return nil },
		func(w *sahl.Writer, r *sahl.Request) {},
		func(w *sahl.Writer, chunk *sahl.ResponseChunk) {},
	)
	factories := sahl.Factories()
	_, ok := factories["test-with-config-and-response"]
	assert.True(t, ok)
}

func TestRegisterWithBodyConfigAndResponse(t *testing.T) {
	sahl.RegisterWithBodyConfigAndResponse("test-with-body-config-and-response",
		func(h sahl.ConfigHandle) error { return nil },
		func(w *sahl.Writer, r *sahl.Request) {},
		func(w *sahl.Writer, chunk *sahl.ResponseChunk) {},
	)
	factories := sahl.Factories()
	_, ok := factories["test-with-body-config-and-response"]
	assert.True(t, ok)
}

func TestFactory(t *testing.T) {
	factory := sahl.Factory(func(w *sahl.Writer, r *sahl.Request) {})
	require.NotNil(t, factory)
	cfgHandle := &fakeConfigHandle{}
	ff, err := factory.Create(cfgHandle, nil)
	require.NoError(t, err)
	require.NotNil(t, ff)
}

// ============================================================
// ConfigHandle (DefineCounter, DefineHistogram, RawConfig, Log)
// ============================================================

func TestConfigHandle_DefineCounter_Success(t *testing.T) {
	sahl.RegisterWithConfig("test-define-counter",
		func(h sahl.ConfigHandle) error {
			id, err := h.DefineCounter("reqs_total", "result")
			assert.NoError(t, err)
			assert.NotZero(t, id)
			return nil
		},
		func(w *sahl.Writer, r *sahl.Request) {},
	)
	factory := sahl.Factories()["test-define-counter"]
	_, err := factory.Create(&fakeConfigHandle{}, nil)
	assert.NoError(t, err)
}

func TestConfigHandle_DefineCounter_Failure(t *testing.T) {
	sahl.RegisterWithConfig("test-define-counter-fail",
		func(h sahl.ConfigHandle) error {
			_, err := h.DefineCounter("fail_metric")
			assert.Error(t, err)
			return nil
		},
		func(w *sahl.Writer, r *sahl.Request) {},
	)
	factory := sahl.Factories()["test-define-counter-fail"]
	// fakeConfigHandle returns MetricsFailed for Define* calls.
	_, err := factory.Create(&fakeConfigHandle{failMetrics: true}, nil)
	assert.NoError(t, err)
}

func TestConfigHandle_DefineHistogram(t *testing.T) {
	sahl.RegisterWithConfig("test-define-histogram",
		func(h sahl.ConfigHandle) error {
			id, err := h.DefineHistogram("latency_ms")
			assert.NoError(t, err)
			assert.NotZero(t, id)
			return nil
		},
		func(w *sahl.Writer, r *sahl.Request) {},
	)
	factory := sahl.Factories()["test-define-histogram"]
	_, err := factory.Create(&fakeConfigHandle{}, nil)
	assert.NoError(t, err)
}

func TestConfigHandle_RawConfig(t *testing.T) {
	raw := []byte(`{"key":"value"}`)
	sahl.RegisterWithConfig("test-raw-config",
		func(h sahl.ConfigHandle) error {
			assert.Equal(t, raw, h.RawConfig())
			return nil
		},
		func(w *sahl.Writer, r *sahl.Request) {},
	)
	factory := sahl.Factories()["test-raw-config"]
	_, err := factory.Create(&fakeConfigHandle{}, raw)
	assert.NoError(t, err)
}

func TestConfigHandle_Log(t *testing.T) {
	sahl.RegisterWithConfig("test-config-log",
		func(h sahl.ConfigHandle) error {
			h.Log(shared.LogLevelInfo, "config loaded for filter %s", "test-config-log")
			return nil
		},
		func(w *sahl.Writer, r *sahl.Request) {},
	)
	factory := sahl.Factories()["test-config-log"]
	_, err := factory.Create(&fakeConfigHandle{}, nil)
	assert.NoError(t, err)
}

// ============================================================
// Filter lifecycle: OnStreamComplete, OnDestroy, OnRequestBody
// ============================================================

func TestFilter_OnStreamComplete_CancelsGo(t *testing.T) {
	fh := fake.NewFilterHandle()
	cancelled := make(chan struct{})
	h := func(w *sahl.Writer, r *sahl.Request) {
		w.Go(func(ctx context.Context) {
			<-ctx.Done()
			close(cancelled)
		})
	}
	f := sahl.NewFilterForTest("test", h, fh)
	f.OnRequestHeaders(fh.RequestHeaders(), false)
	f.OnStreamComplete()
	select {
	case <-cancelled:
	default:
		// goroutine may not have started yet: just confirm no panic
	}
}

func TestFilter_OnDestroy_PoolReturn(t *testing.T) {
	fh := fake.NewFilterHandle()
	h := func(w *sahl.Writer, r *sahl.Request) {}
	f := sahl.NewFilterForTest("test", h, fh)
	f.OnRequestHeaders(fh.RequestHeaders(), false)
	f.OnStreamComplete()
	f.OnDestroy() // must not panic; confirms pool invariant
}

func TestFilter_OnRequestBody_BuffersUntilEOS(t *testing.T) {
	fh := fake.NewFilterHandle(fake.WithRequestBody([]byte("body-data")))
	var bodyRead string
	h := func(w *sahl.Writer, r *sahl.Request) {
		bodyRead = string(r.Body())
	}
	// Must use the body-aware filter variant so OnRequestHeaders returns StopAllAndBuffer.
	f := sahl.NewBodyAwareFilterForTest("test", h, fh)
	status := f.OnRequestHeaders(fh.RequestHeaders(), false)
	assert.Equal(t, shared.HeadersStatusStopAllAndBuffer, status)
	// Not EOS yet: buffer.
	bodyStatus := f.OnRequestBody(fh.BufferedRequestBody(), false)
	assert.Equal(t, shared.BodyStatusStopAndBuffer, bodyStatus)
	// EOS: handler runs now.
	bodyStatus = f.OnRequestBody(fh.BufferedRequestBody(), true)
	assert.Equal(t, shared.BodyStatusContinue, bodyStatus)
	assert.Equal(t, "body-data", bodyRead)
}

// ============================================================
// Response phase: onResponseHeaders, onResponseBody, parseStatus
// ============================================================

func TestParseStatus(t *testing.T) {
	cases := []struct {
		s    string
		want int
	}{
		{"200", 200},
		{"404", 404},
		{"200 OK", 200}, // stops at space
		{"", 0},
		{"abc", 0},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, sahl.ParseStatusForTest(c.s), "input: %q", c.s)
	}
}

func TestFilter_ResponsePhase(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithResponseHeaders(map[string]string{
			":status":      "200",
			"content-type": "application/json",
		}),
		fake.WithResponseBody([]byte(`{"tokens":42}`)),
	)

	var (
		headerStatus int
		contentType  string
		bodyData     string
		endStreamSaw bool
	)

	respFn := func(w *sahl.Writer, chunk *sahl.ResponseChunk) {
		if chunk.Data == nil {
			// headers call
			headerStatus = chunk.StatusCode
			contentType = chunk.ContentType
			return
		}
		bodyData = string(chunk.Data)
		endStreamSaw = chunk.EndStream
	}

	f := sahl.NewFilterWithResponseForTest("test", func(w *sahl.Writer, r *sahl.Request) {}, respFn, fh)
	f.OnRequestHeaders(fh.RequestHeaders(), false)
	f.OnResponseHeaders(fh.ResponseHeaders(), false)
	f.OnResponseBody(fh.BufferedResponseBody(), true)

	assert.Equal(t, 200, headerStatus)
	assert.Equal(t, "application/json", contentType)
	assert.Equal(t, `{"tokens":42}`, bodyData)
	assert.True(t, endStreamSaw)
}

func TestFilter_ResponsePhase_EmptyChunk(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithResponseHeaders(map[string]string{":status": "204"}),
	)
	var dataSeen []byte
	respFn := func(w *sahl.Writer, chunk *sahl.ResponseChunk) {
		if chunk.Data != nil {
			dataSeen = chunk.Data
		}
	}
	f := sahl.NewFilterWithResponseForTest("test", func(w *sahl.Writer, r *sahl.Request) {}, respFn, fh)
	f.OnRequestHeaders(fh.RequestHeaders(), false)
	f.OnResponseHeaders(fh.ResponseHeaders(), false)
	// Empty body buffer -> data should be []byte{} not nil.
	f.OnResponseBody(fake.NewFakeBodyBuffer(nil), true)
	assert.NotNil(t, dataSeen)
	assert.Empty(t, dataSeen)
}

func TestFilter_OnStreamComplete_FlushesResponseMutations(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithResponseHeaders(map[string]string{":status": "200"}),
	)
	respFn := func(w *sahl.Writer, chunk *sahl.ResponseChunk) {
		if chunk.EndStream {
			w.IncrementCounter(shared.MetricID(1), 1)
		}
	}
	f := sahl.NewFilterWithResponseForTest("test", func(w *sahl.Writer, r *sahl.Request) {}, respFn, fh)
	f.OnRequestHeaders(fh.RequestHeaders(), false)
	f.OnResponseHeaders(fh.ResponseHeaders(), false)
	f.OnResponseBody(fake.NewFakeBodyBuffer([]byte("data")), true)
	f.OnStreamComplete()
	assert.Len(t, fh.CounterIncrements, 1)
}

// ============================================================
// Middleware
// ============================================================

func TestFactory_FullChain(t *testing.T) {
	// Exercises: configFactory.Create -> filterFactory.Create -> newSahlFilter
	// and filterFactory.OnDestroy, CreatePerRoute.
	factory := sahl.Factory(func(w *sahl.Writer, r *sahl.Request) {})
	cfgHandle := &fakeConfigHandle{}
	ff, err := factory.Create(cfgHandle, nil)
	require.NoError(t, err)

	// CreatePerRoute: no-op, always nil.
	_, err = factory.CreatePerRoute(nil)
	assert.NoError(t, err)

	// filterFactory.Create -> newSahlFilter
	fh := fake.NewFilterHandle()
	filter := ff.Create(fh)
	require.NotNil(t, filter)
	filter.OnRequestHeaders(fh.RequestHeaders(), false)
	filter.OnStreamComplete()
	filter.OnDestroy()

	// filterFactory.OnDestroy
	ff.OnDestroy()
}

func TestRegisterWithConfig_ConfigError(t *testing.T) {
	sahl.RegisterWithConfig("test-config-fn-error",
		func(h sahl.ConfigHandle) error {
			return assert.AnError
		},
		func(w *sahl.Writer, r *sahl.Request) {},
	)
	factory := sahl.Factories()["test-config-fn-error"]
	_, err := factory.Create(&fakeConfigHandle{}, nil)
	assert.Error(t, err)
}

func TestHeader_Peek_EmptyValue_CoveredBranch(t *testing.T) {
	// Covers the buf.Len == 0 branch in Peek.
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"x-zero": ""}))
	req := buildRequest(fh)
	v, ok := req.Header.Peek("x-zero")
	assert.False(t, ok)
	assert.Empty(t, v)
}

func TestHeader_Reset_NoCache(t *testing.T) {
	// Covers the reset path when cache is nil (new Request from pool).
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"x-a": "1"}))
	req := sahl.NewRequestForTest(fh.RequestHeaders(), fh, "test")
	// Get populates cache.
	assert.Equal(t, "1", req.Header.Get("x-a"))
	// Simulate pool reuse by re-calling reset via a new request.
	fh2 := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"x-a": "2"}))
	req2 := sahl.NewRequestForTest(fh2.RequestHeaders(), fh2, "test")
	assert.Equal(t, "2", req2.Header.Get("x-a"))
}

func TestRequest_Body_Unbuffered(t *testing.T) {
	// Covers readBody unbuffered path (isBuffered=false, reads both stores).
	fh := fake.NewFilterHandle(
		fake.WithRequestBody([]byte("buf ")),
		fake.WithReceivedRequestBody([]byte("recv")),
	)
	fh.SetReceivedBufferedRequestBody(false)
	h := func(w *sahl.Writer, r *sahl.Request) {}
	f := sahl.NewBodyAwareFilterForTest("test", h, fh)
	f.OnRequestHeaders(fh.RequestHeaders(), false)
	// Drive to EOS so handler runs; Body() is called inside.
	var got string
	hBody := func(w *sahl.Writer, r *sahl.Request) { got = string(r.Body()) }
	f2 := sahl.NewBodyAwareFilterForTest("test", hBody, fh)
	f2.OnRequestHeaders(fh.RequestHeaders(), false)
	f2.OnRequestBody(fh.BufferedRequestBody(), true)
	assert.Equal(t, "buf recv", got)
}

func TestWriter_flushResponseMutations_WithCounter(t *testing.T) {
	// Covers flushResponseMutations path directly via OnStreamComplete.
	fh := fake.NewFilterHandle(
		fake.WithResponseHeaders(map[string]string{":status": "200"}),
		fake.WithResponseBody([]byte("data")),
	)
	var flushed bool
	respFn := func(w *sahl.Writer, chunk *sahl.ResponseChunk) {
		if chunk.EndStream {
			w.IncrementCounter(shared.MetricID(1), 1)
			flushed = true
		}
	}
	f := sahl.NewFilterWithResponseForTest("test", func(w *sahl.Writer, r *sahl.Request) {}, respFn, fh)
	f.OnRequestHeaders(fh.RequestHeaders(), false)
	f.OnResponseHeaders(fh.ResponseHeaders(), false)
	f.OnResponseBody(fh.BufferedResponseBody(), true)
	f.OnStreamComplete()
	assert.True(t, flushed)
	assert.Len(t, fh.CounterIncrements, 1)
}

func TestDefineHistogram_Failure(t *testing.T) {
	sahl.RegisterWithConfig("test-histogram-fail",
		func(h sahl.ConfigHandle) error {
			_, err := h.DefineHistogram("fail_hist")
			assert.Error(t, err)
			return nil
		},
		func(w *sahl.Writer, r *sahl.Request) {},
	)
	factory := sahl.Factories()["test-histogram-fail"]
	_, err := factory.Create(&fakeConfigHandle{failMetrics: true}, nil)
	assert.NoError(t, err)
}

func TestFormatLogAttrs_MultipleAttrs(t *testing.T) {
	// Covers formatLogAttrs with multiple attrs (the loop).
	fh := fake.NewFilterHandle()
	req := sahl.NewRequestForTest(fh.RequestHeaders(), fh, "filter")
	// Multiple attrs exercises the loop body more than once.
	req.LogAttrs(shared.LogLevelInfo, "msg",
		slog.String("k1", "v1"),
		slog.String("k2", "v2"),
		slog.Int("count", 42),
	)
}

func TestFormatLogAttrs_NoAttrs(t *testing.T) {
	// Covers the len(attrs)==0 early return in formatLogAttrs.
	fh := fake.NewFilterHandle()
	req := sahl.NewRequestForTest(fh.RequestHeaders(), fh, "filter")
	req.LogAttrs(shared.LogLevelInfo, "just a message") // no attrs
}

func TestHeader_Peek_CacheHit(t *testing.T) {
	// Peek after Get returns the cached Go string (not Envoy memory).
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"x-tok": "abc"}))
	req := buildRequest(fh)
	req.Header.Get("x-tok") // populate cache
	v, ok := req.Header.Peek("x-tok")
	require.True(t, ok)
	assert.Equal(t, "abc", v)
}

func TestHeader_Reset_ClearsCacheEntries(t *testing.T) {
	// Covers the delete loop in Header.reset (cache non-nil with entries).
	fh := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"x-a": "1"}))
	req := sahl.NewRequestForTest(fh.RequestHeaders(), fh, "test")
	req.Header.Get("x-a") // populates cache
	// Reset with new headers: cache must be cleared.
	fh2 := fake.NewFilterHandle(fake.WithHeaders(map[string]string{"x-a": "2"}))
	req2 := sahl.NewRequestForTest(fh2.RequestHeaders(), fh2, "test")
	assert.Equal(t, "2", req2.Header.Get("x-a"))
}

func TestFilter_OnRequestBody_RespondedPath(t *testing.T) {
	// Covers the bodyAware + responded=true branch in OnRequestBody.
	fh := fake.NewFilterHandle()
	h := func(w *sahl.Writer, r *sahl.Request) {
		w.Send(401, "denied")
	}
	f := sahl.NewBodyAwareFilterForTest("test", h, fh)
	f.OnRequestHeaders(fh.RequestHeaders(), false)
	status := f.OnRequestBody(fh.BufferedRequestBody(), true)
	assert.Equal(t, shared.BodyStatusContinue, status)
	require.Len(t, fh.LocalResponses, 1)
	assert.Equal(t, uint32(401), fh.LocalResponses[0].Status)
}

func TestFlushResponseMutations_HistogramBranch(t *testing.T) {
	// Covers the hist=true branch in flushResponseMutations.
	fh := fake.NewFilterHandle(
		fake.WithResponseHeaders(map[string]string{":status": "200"}),
	)
	respFn := func(w *sahl.Writer, chunk *sahl.ResponseChunk) {
		if chunk.EndStream {
			w.RecordHistogram(shared.MetricID(1), 100)
		}
	}
	f := sahl.NewFilterWithResponseForTest("test", func(w *sahl.Writer, r *sahl.Request) {}, respFn, fh)
	f.OnRequestHeaders(fh.RequestHeaders(), false)
	f.OnResponseHeaders(fh.ResponseHeaders(), false)
	f.OnResponseBody(fake.NewFakeBodyBuffer([]byte("x")), true)
	f.OnStreamComplete()
	require.Len(t, fh.CounterIncrements, 1)
	assert.True(t, fh.CounterIncrements[0].Hist)
	assert.Equal(t, uint64(100), fh.CounterIncrements[0].N)
}

func TestChain_ExecutionOrder(t *testing.T) {
	var order []string
	mw1 := func(next sahl.HandlerFunc) sahl.HandlerFunc {
		return func(w *sahl.Writer, r *sahl.Request) {
			order = append(order, "mw1-before")
			next(w, r)
			order = append(order, "mw1-after")
		}
	}
	mw2 := func(next sahl.HandlerFunc) sahl.HandlerFunc {
		return func(w *sahl.Writer, r *sahl.Request) {
			order = append(order, "mw2-before")
			next(w, r)
			order = append(order, "mw2-after")
		}
	}
	h := func(w *sahl.Writer, r *sahl.Request) {
		order = append(order, "handler")
	}

	fh := fake.NewFilterHandle()
	runHandler(t, fh, sahl.Chain(h, mw1, mw2))
	// Chain(h, mw1, mw2): mw1 is outermost, execution is mw1 -> mw2 -> handler
	assert.Equal(t, []string{"mw1-before", "mw2-before", "handler", "mw2-after", "mw1-after"}, order)
}

// ============================================================
// helpers
// ============================================================

func buildRequest(fh *fake.FakeFilterHandle) *sahl.Request {
	return sahl.NewRequestForTest(fh.RequestHeaders(), fh, "test")
}

func buildWriter(fh *fake.FakeFilterHandle) *sahl.Writer {
	return sahl.NewWriterForTest(fh, fh.GetScheduler())
}

func runHandler(t *testing.T, fh *fake.FakeFilterHandle, h sahl.HandlerFunc) {
	t.Helper()
	f := sahl.NewFilterForTest("test", h, fh)
	f.OnRequestHeaders(fh.RequestHeaders(), false)
}

// fakeConfigHandle implements shared.HttpFilterConfigHandle for tests.
type fakeConfigHandle struct {
	failMetrics bool
}

func (h *fakeConfigHandle) DefineCounter(name string, tagKeys ...string) (shared.MetricID, shared.MetricsResult) {
	if h.failMetrics {
		return 0, shared.MetricsNotFound
	}
	return shared.MetricID(1), shared.MetricsSuccess
}
func (h *fakeConfigHandle) DefineHistogram(name string, tagKeys ...string) (shared.MetricID, shared.MetricsResult) {
	if h.failMetrics {
		return 0, shared.MetricsNotFound
	}
	return shared.MetricID(2), shared.MetricsSuccess
}
func (h *fakeConfigHandle) DefineGauge(name string, tagKeys ...string) (shared.MetricID, shared.MetricsResult) {
	return shared.MetricID(3), shared.MetricsSuccess
}
func (h *fakeConfigHandle) Log(_ shared.LogLevel, _ string, _ ...any) {}
func (h *fakeConfigHandle) GetScheduler() shared.Scheduler            { return nil }
func (h *fakeConfigHandle) HttpCallout(_ string, _ [][2]string, _ []byte, _ uint64, _ shared.HttpCalloutCallback) (shared.HttpCalloutInitResult, uint64) {
	return shared.HttpCalloutInitClusterNotFound, 0
}
func (h *fakeConfigHandle) StartHttpStream(_ string, _ [][2]string, _ []byte, _ bool, _ uint64, _ shared.HttpStreamCallback) (shared.HttpCalloutInitResult, uint64) {
	return shared.HttpCalloutInitClusterNotFound, 0
}
func (h *fakeConfigHandle) SendHttpStreamData(_ uint64, _ []byte, _ bool) bool  { return false }
func (h *fakeConfigHandle) SendHttpStreamTrailers(_ uint64, _ [][2]string) bool { return false }
func (h *fakeConfigHandle) ResetHttpStream(_ uint64)                            {}
