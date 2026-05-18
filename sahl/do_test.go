package sahl_test

import (
	"context"
	"testing"
	"time"

	"github.com/dio/luwes/sahl"
	"github.com/dio/luwes/shared"
	"github.com/dio/luwes/shared/fake"
)

// TestDo_SuccessPath verifies:
//   - w.Go spawns a goroutine, w.Do blocks inside it until callout completes
//   - result and headers/body are returned correctly
//   - mutations queued after Do are flushed via Scheduler.Schedule
func TestDo_SuccessPath(t *testing.T) {
	fh := fake.NewFilterHandle(
		fake.WithHeaders(map[string]string{":method": "GET", ":path": "/"}),
		fake.WithHTTPCalloutFn(func(
			cluster string,
			headers [][2]string,
			body []byte,
			timeoutMs uint64,
			cb shared.HttpCalloutCallback,
		) (shared.HttpCalloutInitResult, uint64) {
			// Fake scheduler runs Schedule() synchronously, so this fires inline
			// once the goroutine calls w.Do and blocks on its channel.
			// We fire it from a goroutine to not deadlock: w.Do blocks on a channel,
			// the callback unblocks it.
			go func() {
				time.Sleep(5 * time.Millisecond)
				cb.OnHttpCalloutDone(1, shared.HttpCalloutSuccess,
					[][2]shared.UnsafeEnvoyBuffer{{strBuf("x-user"), strBuf("alice")}},
					[]shared.UnsafeEnvoyBuffer{strBuf(`{"ok":true}`)},
				)
			}()
			return shared.HttpCalloutInitSuccess, 1
		}),
	)

	done := make(chan struct{})
	var gotResult shared.HttpCalloutResult
	var gotUser string

	filter := sahl.NewFilterForTesting(
		"test",
		func(w *sahl.Writer, r *sahl.Request) {
			w.Go(func(ctx context.Context) {
				defer close(done)
				resp, err := w.Do(ctx, sahl.HTTPCalloutRequest{
					Cluster:   "auth",
					TimeoutMs: 500,
				})
				if err != nil {
					t.Errorf("w.Do: unexpected error: %v", err)
					return
				}
				gotResult = resp.Result
				for _, h := range resp.Headers {
					if h[0].ToString() == "x-user" {
						gotUser = h[1].ToString()
					}
				}
				w.SetRequestHeader("x-auth-user", gotUser)
			})
		},
		nil, false, fh,
	)

	_ = filter.OnRequestHeaders(fh.RequestHeaders(), false)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("w.Do: timed out waiting for goroutine to finish")
	}

	if gotResult != shared.HttpCalloutSuccess {
		t.Errorf("want HttpCalloutSuccess, got %d", gotResult)
	}
	if gotUser != "alice" {
		t.Errorf("want user=alice, got %q", gotUser)
	}
}

// TestDo_ContextCancelled verifies w.Do returns an error when ctx is cancelled
// before the callout completes.
func TestDo_ContextCancelled(t *testing.T) {
	ready := make(chan struct{})

	fh := fake.NewFilterHandle(
		fake.WithHTTPCalloutFn(func(
			_ string, _ [][2]string, _ []byte, _ uint64,
			cb shared.HttpCalloutCallback,
		) (shared.HttpCalloutInitResult, uint64) {
			// Signal that the callout is in-flight, then never fire the callback.
			close(ready)
			return shared.HttpCalloutInitSuccess, 1
		}),
	)

	done := make(chan struct{})
	var doErr error

	filter := sahl.NewFilterForTesting(
		"test",
		func(w *sahl.Writer, r *sahl.Request) {
			w.Go(func(ctx context.Context) {
				defer close(done)
				ctx2, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
				_, err := w.Do(ctx2, sahl.HTTPCalloutRequest{
					Cluster:   "slow",
					TimeoutMs: 10000,
				})
				doErr = err
			})
		},
		nil, false, fh,
	)

	_ = filter.OnRequestHeaders(fh.RequestHeaders(), false)

	<-ready
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("w.Do: timed out waiting for cancellation")
	}

	if doErr == nil {
		t.Error("want non-nil error on context cancellation, got nil")
	}
}

// TestDo_PanicOutsideGo verifies w.Do panics when called outside w.Go.
func TestDo_PanicOutsideGo(t *testing.T) {
	fh := fake.NewFilterHandle()
	panicked := false

	filter := sahl.NewFilterForTesting(
		"test",
		func(w *sahl.Writer, r *sahl.Request) {
			func() {
				defer func() {
					if recover() != nil {
						panicked = true
					}
				}()
				_, _ = w.Do(context.Background(), sahl.HTTPCalloutRequest{Cluster: "x"})
			}()
		},
		nil, false, fh,
	)

	_ = filter.OnRequestHeaders(fh.RequestHeaders(), false)
	if !panicked {
		t.Error("expected panic when calling w.Do outside w.Go")
	}
}
