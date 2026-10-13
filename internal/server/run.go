package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"po-trans/docs"
	"po-trans/internal/app"
	"po-trans/internal/config"
	"po-trans/internal/httpapi"
	"po-trans/internal/repository/postgres"
)

// startupPingTimeout limits the first database connection at start.
const startupPingTimeout = 10 * time.Second

// Run builds the service from cfg and serves until ctx is cancelled. A nil
// return means a clean shutdown, including a stop requested while starting up.
func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	// From here the pool belongs to Serve, which closes it after the server.
	health := postgres.NewHealthCheck(pool)
	pingCtx, cancel := context.WithTimeout(ctx, startupPingTimeout)
	err = health.Ping(pingCtx)
	cancel()
	if err != nil {
		pool.Close()
		if ctx.Err() != nil {
			return nil // asked to stop while connecting
		}
		return fmt.Errorf("connect to database: %w", err)
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		pool.Close()
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr, err)
	}

	writer := postgres.NewTransferRepository(pool, postgres.DefaultRetryPolicy(), logger)
	handler := httpapi.NewHandler(httpapi.Deps{
		App:    app.New(writer, postgres.NewQueryRepository(pool)),
		Pinger: health,
	}, handlerOptions(cfg, logger))

	logger.Info("listening", "addr", ln.Addr().String())
	return Serve(ctx, logger, newServer(handler, cfg, logger), ln, cfg.ShutdownTimeout, pool.Close)
}

// handlerOptions configures the handler chain from cfg. Swagger UI at /docs is
// served only with DOCS_ENABLED: it is for a quick check of the demo.
func handlerOptions(cfg config.Config, logger *slog.Logger) httpapi.Options {
	opts := httpapi.Options{RequestTimeout: cfg.RequestTimeout, Logger: logger}
	if cfg.DocsEnabled {
		opts.OpenAPISpec = docs.OpenAPI
	}
	return opts
}

// newServer configures the HTTP server. Request contexts are rooted in
// context.Background(), not in the application context: cancelling the
// application starts the graceful shutdown and must not abort a transfer in
// flight. The server's own errors go to the structured logger.
func newServer(handler http.Handler, cfg config.Config, logger *slog.Logger) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		BaseContext:       func(net.Listener) context.Context { return context.Background() },
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
}
