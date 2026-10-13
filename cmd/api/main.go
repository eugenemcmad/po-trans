// Command api runs the coin transfer service.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"po-trans/internal/config"
	"po-trans/internal/server"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		logger.Error("load configuration", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err = server.Run(ctx, cfg, logger)
	stop()
	if err != nil {
		logger.Error("service stopped with error", "error", err)
		os.Exit(1)
	}
}
