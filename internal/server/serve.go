// Package server wires the service together and runs it until its context ends.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Serve runs srv on ln until ctx is cancelled, then stops it gracefully:
//
//  1. srv.Shutdown with its own context limited to shutdownTimeout — no new
//     connections, started requests finish;
//  2. if the timeout expires, srv.Close;
//  3. only then the closers run (the database pool among them), so no
//     transaction is cut off by closing the pool.
//
// Request contexts must not derive from ctx (see Run); otherwise cancelling ctx
// would abort the very requests Shutdown is waiting for.
func Serve(ctx context.Context, logger *slog.Logger, srv *http.Server, ln net.Listener, shutdownTimeout time.Duration, closers ...func()) error {
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	var result error
	select {
	case err := <-serveErr:
		// The server failed on its own; there is nothing to shut down.
		result = fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed, closing connections", "error", err)
			result = fmt.Errorf("graceful shutdown: %w", err)
			if closeErr := srv.Close(); closeErr != nil {
				logger.Error("close http server", "error", closeErr)
			}
		}
		if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			result = errors.Join(result, fmt.Errorf("http server: %w", err))
		}
	}

	for _, closeFn := range closers {
		closeFn()
	}
	return result
}
