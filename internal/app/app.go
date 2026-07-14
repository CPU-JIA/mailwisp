// Package app composes and runs the MailWisp process.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"mailwisp/internal/config"
	"mailwisp/internal/httpapi"
)

// App owns the process lifecycle and concrete service composition.
type App struct {
	config config.Config
	logger *slog.Logger
	http   *httpapi.Server
}

// New creates a fully composed MailWisp application.
func New(cfg config.Config, logger *slog.Logger) (*App, error) {
	if logger == nil {
		return nil, errors.New("logger is required")
	}
	return &App{
		config: cfg,
		logger: logger,
		http:   httpapi.NewServer(cfg.HTTP, logger),
	}, nil
}

// Run starts all services, waits for cancellation or failure, and shuts down.
func (a *App) Run(ctx context.Context) error {
	serverErrors := make(chan error, 1)
	a.http.SetReady(true)
	go func() {
		serverErrors <- a.http.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		a.logger.Info("shutdown requested")
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.config.ShutdownTimeout)
	defer cancel()
	if err := a.http.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown http server: %w", err)
	}

	select {
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server after shutdown: %w", err)
		}
	case <-time.After(a.config.ShutdownTimeout):
		return errors.New("http server did not stop before shutdown deadline")
	}
	a.logger.Info("shutdown complete")
	return nil
}
