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
	"time"

	apihttp "github.com/djsega1/plata-test-task/internal/api/http"
	"github.com/djsega1/plata-test-task/internal/config"
	"github.com/djsega1/plata-test-task/internal/storage/postgres"
	"github.com/djsega1/plata-test-task/internal/usecase/quotes"
	"github.com/djsega1/plata-test-task/pkg/clock"
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

	if err := postgres.Migrate(context.Background(), cfg.DatabaseURL); err != nil {
		logger.Error("migrate", "error", err)
		return 1
	}

	pool, err := postgres.NewPool(
		context.Background(), cfg.DatabaseURL, cfg.PostgresMaxConns, cfg.PostgresMaxConnLifetime, cfg.PostgresHealthCheckPeriod,
	)
	if err != nil {
		logger.Error("connect to database", "error", err)
		return 1
	}
	defer pool.Close()

	provider, err := newRateProvider(cfg)
	if err != nil {
		logger.Error("provider", "error", err)
		return 1
	}

	repo := postgres.NewRepository(pool)
	sysClock := clock.NewSystemClock()
	limiter := quotes.NewRateLimiter(cfg.RateLimitPerMinute, cfg.RateLimitPerHour)
	worker := quotes.NewWorker(
		repo, provider, sysClock, limiter, logger, cfg.DispatchBaseBackoff, cfg.DispatchMaxBackoff, cfg.DispatchMaxAttempts,
	)
	dispatcher := quotes.NewDispatcher(
		repo, sysClock, worker, logger, cfg.DispatchBatchSize, cfg.DispatchPoolSize,
		cfg.DispatchVisibilityTimeout, cfg.DispatchPassTimeout,
	)

	// Buffered by one: a POST during an in-progress pass still leaves a
	// pending signal for the next one, without blocking the HTTP response.
	nudge := make(chan struct{}, 1)
	ticker := time.NewTicker(cfg.DispatchTickInterval)
	defer ticker.Stop()

	dispatchCtx, stopDispatch := context.WithCancel(context.Background())
	dispatchDone := make(chan struct{})
	go func() {
		defer close(dispatchDone)
		dispatcher.Run(dispatchCtx, nudge, ticker.C)
	}()
	// stopDispatch only stops Run from starting another pass; a pass already
	// in flight keeps running, so waiting on dispatchDone here lets it
	// finish instead of abandoning claimed rows mid-flight. That wait is
	// itself bounded by ShutdownTimeout: a pass may legitimately run up to
	// DispatchPassTimeout (90s by default), well past a typical container
	// terminationGracePeriodSeconds — abandoning it past the deadline is
	// safe (delivery is at-least-once; the reaper reclaims it), whereas
	// blocking here past that grace period trades a clean shutdown for a
	// SIGKILL that skips it entirely.
	defer func() {
		stopDispatch()
		select {
		case <-dispatchDone:
		case <-time.After(cfg.ShutdownTimeout):
			logger.Warn("dispatcher drain timed out; abandoning in-flight pass, reaper will reclaim its rows",
				"timeout", cfg.ShutdownTimeout)
		}
	}()

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           apihttp.NewRouter(logger, pool.Ping, repo, sysClock, nudge),
		ReadTimeout:       cfg.ReadTimeout,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("server starting", "addr", cfg.HTTPAddr, "provider", cfg.Provider, "storage", cfg.Storage)
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
