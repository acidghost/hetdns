// Package app wires the concrete service and owns its lifecycle.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/acidghost/hetdns/internal/buildinfo"
	"github.com/acidghost/hetdns/internal/config"
	"github.com/acidghost/hetdns/internal/hetzner"
	"github.com/acidghost/hetdns/internal/reconcile"
	"github.com/acidghost/hetdns/internal/scheduler"
	"github.com/acidghost/hetdns/internal/source"
	"github.com/acidghost/hetdns/internal/status"
	"github.com/acidghost/hetdns/internal/telemetry"
	"github.com/acidghost/hetdns/internal/web"
)

// Options are already validated startup inputs.
type Options struct {
	Config       *config.Config
	Token        string
	Interval     time.Duration
	Listen       string
	HistoryLimit int
	Build        buildinfo.Info
	Logger       *slog.Logger
}

// Run serves until context cancellation or an unexpected component failure.
func Run(ctx context.Context, options Options) error {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}

	userAgent := "hetdns/" + options.Build.Version
	sources, err := source.New(options.Config.Sources, userAgent)
	if err != nil {
		return err
	}
	defer source.CloseIdleConnections(sources)

	store := status.New(options.Build, options.Config, options.HistoryLimit)
	metrics, err := telemetry.New(ctx, options.Build, store, options.Logger)
	if err != nil {
		return fmt.Errorf("initialize telemetry: %w", err)
	}
	provider := hetzner.New(options.Token, userAgent)
	reconciler := reconcile.New(
		options.Config,
		sources,
		provider,
		store,
		metrics,
		options.Logger,
	)
	scheduled := scheduler.New(options.Interval, reconciler, store)
	server, err := web.New(options.Listen, store, options.Logger)
	if err != nil {
		return fmt.Errorf("initialize web server: %w", err)
	}
	listener, err := net.Listen("tcp", options.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", options.Listen, err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	serverErrors := make(chan error, 1)
	schedulerErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.Serve(listener)
	}()
	go func() {
		schedulerErrors <- scheduled.Run(runCtx)
	}()

	store.SetReady(true)
	options.Logger.Info(
		"service ready",
		"component", "app",
		"listen", options.Listen,
		"version", options.Build.Version,
	)

	var runErr error
	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			runErr = fmt.Errorf("HTTP server stopped: %w", err)
		}
	case err := <-schedulerErrors:
		if err != nil && ctx.Err() == nil {
			runErr = fmt.Errorf("scheduler stopped: %w", err)
		} else if err == nil && ctx.Err() == nil {
			runErr = errors.New("scheduler stopped unexpectedly")
		}
	}

	store.SetReady(false)
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil && runErr == nil {
		runErr = fmt.Errorf("shutdown HTTP server: %w", err)
	}
	if err := metrics.Shutdown(shutdownCtx); err != nil {
		options.Logger.Warn(
			"telemetry shutdown failed",
			"component", "telemetry",
		)
	}

	options.Logger.Info("service stopped", "component", "app")
	return runErr
}
