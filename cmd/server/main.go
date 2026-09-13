// Command server wires config, adapters and the HTTP server together.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	apihttp "github.com/djsega1/plata-test-task/internal/api/http"
	"github.com/djsega1/plata-test-task/internal/config"
)

func main() {
	os.Exit(run())
}

func run() int {
	bootLogger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load(os.Args[1:], os.Getenv)
	if err != nil {
		bootLogger.Error("config", "error", err)
		return 1
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           apihttp.NewRouter(logger),
		ReadTimeout:       cfg.ReadTimeout,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("server starting", "addr", cfg.HTTPAddr, "provider", cfg.Provider)
		serveErr <- srv.ListenAndServe()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			return 1
		}
	case <-ctx.Done():
		logger.Info("shutdown signal received")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
			return 1
		}
	}

	logger.Info("server stopped")
	return 0
}
