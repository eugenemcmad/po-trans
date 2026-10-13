package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"po-trans/docs"
	"po-trans/internal/app"
	"po-trans/internal/config"
	"po-trans/internal/httpapi"
	"po-trans/tests/memstore"
)

// TestMain fails the package on leaked goroutines.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// wait is a generous deadline for events that must happen; it only matters
// when a test fails, so slow machines do not make tests flaky.
const wait = 30 * time.Second

var testLogger = slog.New(slog.DiscardHandler)

const transferJSON = `{"idempotency_key":"k","from_account_id":"acc_1","to_account_id":"acc_2","amount":10}`

type fixture struct {
	store   *memstore.Store
	addr    string
	srv     *http.Server
	ln      net.Listener
	client  *http.Client
	entered chan struct{} // closed when a transfer reaches the store
	release chan struct{} // closed to let the slow transfer finish
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{store: memstore.Seeded(), entered: make(chan struct{}), release: make(chan struct{})}
	var once atomic.Bool
	f.store.BeforeTransfer = func(context.Context) {
		if once.CompareAndSwap(false, true) {
			close(f.entered)
			<-f.release
		}
	}
	handler := httpapi.NewHandler(httpapi.Deps{
		App:    app.New(f.store, f.store),
		Pinger: f.store,
	}, httpapi.Options{RequestTimeout: wait, Logger: testLogger})

	f.srv = newServer(handler, config.Config{
		ReadHeaderTimeout: wait, ReadTimeout: wait, WriteTimeout: wait, IdleTimeout: wait,
	}, testLogger)
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	f.ln, f.addr = ln, "http://"+ln.Addr().String()
	f.client = &http.Client{Transport: &http.Transport{}}
	t.Cleanup(f.client.CloseIdleConnections)
	return f
}

func (f *fixture) postTransfer(ctx context.Context) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.addr+"/api/v1/transfers", strings.NewReader(transferJSON))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := f.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = res.Body.Close() }()
	_, err = io.Copy(io.Discard, res.Body)
	return res.StatusCode, err
}

// TestServe_GracefulShutdown: on stop, a transfer in flight completes with 200, new connections are refused, and the pool closes last.
func TestServe_GracefulShutdown(t *testing.T) {
	f := newFixture(t)
	appCtx, stop := context.WithCancel(t.Context())
	defer stop()

	var requestFinished atomic.Bool
	var closedAfterRequest atomic.Bool
	closer := func() { closedAfterRequest.Store(requestFinished.Load()) }

	served := make(chan error, 1)
	start := time.Now()
	go func() { served <- Serve(appCtx, testLogger, f.srv, f.ln, 15*time.Second, closer) }()

	type result struct {
		code int
		err  error
	}
	slow := make(chan result, 1)
	go func() {
		code, err := f.postTransfer(t.Context())
		requestFinished.Store(true)
		slow <- result{code, err}
	}()

	// A request is in flight; now the application is asked to stop.
	select {
	case <-f.entered:
	case <-time.After(wait):
		t.Fatal("slow request never reached the store")
	}
	stop()

	// New connections are refused once the shutdown has begun.
	require.Eventually(t, func() bool {
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", f.ln.Addr().String())
		if err == nil {
			_ = conn.Close()
			return false
		}
		return true
	}, wait, 10*time.Millisecond, "listener must stop accepting")

	select {
	case err := <-served:
		t.Fatalf("Serve returned while a request was in flight: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	// The in-flight request is not cancelled by the application context.
	close(f.release)
	res := <-slow
	require.NoError(t, res.err)
	require.Equal(t, http.StatusOK, res.code)

	select {
	case err := <-served:
		require.NoError(t, err)
	case <-time.After(wait):
		t.Fatal("Serve did not return after the request finished")
	}
	require.Less(t, time.Since(start), 15*time.Second, "within the shutdown limit")
	require.True(t, closedAfterRequest.Load(), "closers run only after the server has stopped")
	require.Equal(t, int64(990), f.store.Balance("acc_1"), "the transfer was completed, not aborted")
}

// TestServe_ShutdownTimeoutForcesClose: a request stuck past the shutdown limit is cut off, and the closers still run.
func TestServe_ShutdownTimeoutForcesClose(t *testing.T) {
	f := newFixture(t)
	appCtx, stop := context.WithCancel(t.Context())
	defer stop()

	var closed atomic.Bool
	served := make(chan error, 1)
	go func() {
		served <- Serve(appCtx, testLogger, f.srv, f.ln, 100*time.Millisecond, func() { closed.Store(true) })
	}()

	clientErr := make(chan error, 1)
	go func() {
		_, err := f.postTransfer(t.Context())
		clientErr <- err
	}()
	select {
	case <-f.entered:
	case <-time.After(wait):
		t.Fatal("slow request never reached the store")
	}
	stop()

	select {
	case err := <-served:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(wait):
		t.Fatal("Serve did not give up after the shutdown timeout")
	}
	require.True(t, closed.Load(), "closers run even after a forced close")

	require.Error(t, <-clientErr, "the connection was closed under the stuck request")
	close(f.release) // let the handler goroutine finish
}

// TestServe_ServerFailure: a server that fails on its own returns the error and still runs the closers.
func TestServe_ServerFailure(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.ln.Close()) // Serve on a closed listener fails at once

	var closed atomic.Bool
	err := Serve(t.Context(), testLogger, f.srv, f.ln, time.Second, func() { closed.Store(true) })

	require.Error(t, err)
	require.NotErrorIs(t, err, http.ErrServerClosed)
	require.True(t, closed.Load())
}

// DOCS_ENABLED decides whether the handler built by Run serves Swagger UI.
func TestHandlerOptions_Docs(t *testing.T) {
	tests := []struct {
		name        string
		docsEnabled bool
		wantCode    int
	}{
		{name: "enabled", docsEnabled: true, wantCode: http.StatusOK},
		{name: "disabled by default", docsEnabled: false, wantCode: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{RequestTimeout: wait, DocsEnabled: tt.docsEnabled}
			opts := handlerOptions(cfg, testLogger)
			store := memstore.Seeded()
			h := httpapi.NewHandler(httpapi.Deps{App: app.New(store, store), Pinger: store}, opts)

			for _, path := range []string{"/docs", "/docs/openapi.yaml"} {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody))
				require.Equal(t, tt.wantCode, rec.Code, path)
			}
			if tt.docsEnabled {
				require.Equal(t, docs.OpenAPI, opts.OpenAPISpec, "the embedded docs/openapi.yaml is served")
			} else {
				require.Empty(t, opts.OpenAPISpec)
			}
			require.Equal(t, wait, opts.RequestTimeout)
			require.Same(t, testLogger, opts.Logger)
		})
	}
}
