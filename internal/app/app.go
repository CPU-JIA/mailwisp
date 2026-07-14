// Package app composes and runs the MailWisp process.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"mailwisp/internal/config"
	"mailwisp/internal/contentstore"
	"mailwisp/internal/httpapi"
	"mailwisp/internal/lmtp"
	"mailwisp/internal/message"
	"mailwisp/internal/postgres"
)

// App owns the process lifecycle and concrete service composition.
type App struct {
	config     config.Config
	logger     *slog.Logger
	http       *httpapi.Server
	lmtp       *lmtp.Server
	pool       *pgxpool.Pool
	repository *postgres.DeliveryRepository
}

type serviceResult struct {
	name string
	err  error
}

// New creates a fully composed MailWisp application.
func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*App, error) {
	if logger == nil {
		return nil, errors.New("logger is required")
	}
	poolConfig, err := pgxpool.ParseConfig(cfg.Postgres.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse postgres pool configuration: %w", err)
	}
	poolConfig.MinConns = cfg.Postgres.MinConnections
	poolConfig.MaxConns = cfg.Postgres.MaxConnections
	poolConfig.ConnConfig.ConnectTimeout = cfg.Postgres.ConnectTimeout
	poolConfig.HealthCheckPeriod = 30 * time.Second
	poolConfig.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	cleanupPool := true
	defer func() {
		if cleanupPool {
			pool.Close()
		}
	}()

	store, err := contentstore.Open(cfg.Content.Root, contentstore.Options{MaxBytes: cfg.Content.MaxBytes})
	if err != nil {
		return nil, fmt.Errorf("open content store: %w", err)
	}
	repository, err := postgres.NewDeliveryRepository(pool)
	if err != nil {
		return nil, fmt.Errorf("create delivery repository: %w", err)
	}
	receiver, err := message.NewReceiver(store, repository)
	if err != nil {
		return nil, fmt.Errorf("create message receiver: %w", err)
	}
	lmtpServer, err := lmtp.NewServer(lmtp.Options{
		Hostname:         cfg.LMTP.Hostname,
		MaxMessageBytes:  cfg.LMTP.MaxMessageBytes,
		MaxCommandBytes:  cfg.LMTP.MaxCommandBytes,
		MaxDataLineBytes: cfg.LMTP.MaxDataLineBytes,
		MaxRecipients:    cfg.LMTP.MaxRecipients,
		MaxSessions:      cfg.LMTP.MaxSessions,
		SessionTimeout:   cfg.LMTP.SessionTimeout,
		DeliveryTimeout:  cfg.LMTP.DeliveryTimeout,
	}, repository, receiver, logger)
	if err != nil {
		return nil, fmt.Errorf("create LMTP server: %w", err)
	}
	httpServer := httpapi.NewServer(cfg.HTTP, logger)
	httpServer.SetReadinessChecker(repository)

	cleanupPool = false
	return &App{
		config:     cfg,
		logger:     logger,
		http:       httpServer,
		lmtp:       lmtpServer,
		pool:       pool,
		repository: repository,
	}, nil
}

// Run starts all services, waits for cancellation or failure, and shuts down.
func (a *App) Run(ctx context.Context) error {
	defer a.pool.Close()
	startupContext, startupCancel := context.WithTimeout(ctx, a.config.Postgres.ConnectTimeout)
	defer startupCancel()
	if err := a.repository.Ready(startupContext); err != nil {
		return fmt.Errorf("verify postgres readiness: %w", err)
	}
	startupCancel()

	lmtpListener, err := net.Listen("tcp", a.config.LMTP.Addr)
	if err != nil {
		return fmt.Errorf("listen for LMTP: %w", err)
	}
	a.logger.Info("LMTP server listening", "addr", lmtpListener.Addr().String())

	serviceContext, cancelServices := context.WithCancel(ctx)
	defer cancelServices()
	results := make(chan serviceResult, 2)
	go func() {
		results <- serviceResult{name: "http", err: a.http.ListenAndServe()}
	}()
	go func() {
		results <- serviceResult{name: "lmtp", err: a.lmtp.Serve(serviceContext, lmtpListener)}
	}()
	a.http.SetReady(true)

	var runError error
	receivedResults := 0
	select {
	case <-ctx.Done():
		a.logger.Info("shutdown requested")
	case result := <-results:
		receivedResults++
		if ctx.Err() != nil {
			a.logger.Info("shutdown requested")
		} else {
			runError = unexpectedServiceError(result)
		}
	}

	a.http.SetReady(false)
	cancelServices()
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), a.config.ShutdownTimeout)
	defer shutdownCancel()
	if err := a.http.Shutdown(shutdownContext); err != nil {
		runError = errors.Join(runError, fmt.Errorf("shutdown HTTP server: %w", err))
		_ = a.http.Close()
	}

	for receivedResults < 2 {
		select {
		case result := <-results:
			receivedResults++
			if err := stoppedServiceError(result); err != nil {
				runError = errors.Join(runError, err)
			}
		case <-shutdownContext.Done():
			_ = a.http.Close()
			return errors.Join(runError, errors.New("services did not stop before shutdown deadline"))
		}
	}

	if runError != nil {
		return runError
	}
	a.logger.Info("shutdown complete")
	return nil
}

func unexpectedServiceError(result serviceResult) error {
	if result.err == nil {
		return fmt.Errorf("%s service stopped unexpectedly", result.name)
	}
	return fmt.Errorf("%s service: %w", result.name, result.err)
}

func stoppedServiceError(result serviceResult) error {
	if result.err == nil || (result.name == "http" && errors.Is(result.err, http.ErrServerClosed)) {
		return nil
	}
	return fmt.Errorf("%s service during shutdown: %w", result.name, result.err)
}
