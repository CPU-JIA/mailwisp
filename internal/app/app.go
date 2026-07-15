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

	"mailwisp/internal/auth"
	"mailwisp/internal/cloudflaretemp"
	"mailwisp/internal/config"
	"mailwisp/internal/contentstore"
	"mailwisp/internal/duckmail"
	"mailwisp/internal/httpapi"
	"mailwisp/internal/jobs"
	"mailwisp/internal/lmtp"
	"mailwisp/internal/mail"
	"mailwisp/internal/mailbox"
	"mailwisp/internal/message"
	"mailwisp/internal/postgres"
	"mailwisp/internal/telemetry"
	"mailwisp/internal/yyds"
)

// App owns the process lifecycle and concrete service composition.
type App struct {
	config       config.Config
	logger       *slog.Logger
	http         *httpapi.Server
	lmtp         *lmtp.Server
	pool         *pgxpool.Pool
	repository   *postgres.DeliveryRepository
	parserWorker *jobs.ParserWorker
	retention    *jobs.Retention
	mailbox      *mailbox.Service
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
	pool, err := openPostgresPool(ctx, cfg.Postgres)
	if err != nil {
		return nil, err
	}
	cleanupPool := true
	defer func() {
		if cleanupPool {
			pool.Close()
		}
	}()

	store, err := contentstore.Open(cfg.Content.Root, contentstore.Options{
		MaxBytes: cfg.Content.MaxBytes, MinFreeBytes: cfg.Content.MinFreeBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("open content store: %w", err)
	}
	metrics := telemetry.NewMetrics(pool)
	repository, err := postgres.NewDeliveryRepositoryWithLimits(pool, postgres.DeliveryLimits{
		MaxInboxMessages: cfg.Inbox.MaxMessages, MaxInboxStorageBytes: cfg.Inbox.MaxStorageBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("create delivery repository: %w", err)
	}
	receiver, err := message.NewReceiver(store, repository)
	if err != nil {
		return nil, fmt.Errorf("create message receiver: %w", err)
	}
	parseRepository, err := postgres.NewContentParseRepository(pool)
	if err != nil {
		return nil, fmt.Errorf("create content parse repository: %w", err)
	}
	capabilityRepository, err := postgres.NewInboxCapabilityRepository(pool)
	if err != nil {
		return nil, fmt.Errorf("create Inbox capability repository: %w", err)
	}
	capabilityService, err := auth.NewCapabilityService(capabilityRepository)
	if err != nil {
		return nil, fmt.Errorf("create Inbox capability service: %w", err)
	}
	mailboxRepository, err := postgres.NewMailboxRepository(pool)
	if err != nil {
		return nil, fmt.Errorf("create Inbox repository: %w", err)
	}
	parserLimits := mail.DefaultLimits()
	parserLimits.MaxRawBytes = cfg.LMTP.MaxMessageBytes
	parser, err := mail.NewParser(parserLimits)
	if err != nil {
		return nil, fmt.Errorf("create MIME parser: %w", err)
	}
	mailboxService, err := mailbox.NewService(mailboxRepository, capabilityService, store, mailbox.Options{
		PublicDomains:    cfg.Inbox.PublicDomains,
		DefaultTTL:       cfg.Inbox.DefaultTTL,
		MaxTTL:           cfg.Inbox.MaxTTL,
		AttachmentParser: parser,
	})
	if err != nil {
		return nil, fmt.Errorf("create Inbox service: %w", err)
	}
	parserWorker, err := jobs.NewParserWorker(parseRepository, store, parser, logger, jobs.ParserOptions{
		Workers:       cfg.Parser.Workers,
		PollInterval:  cfg.Parser.PollInterval,
		ParseTimeout:  cfg.Parser.ParseTimeout,
		LeaseDuration: cfg.Parser.LeaseDuration,
		MaxAttempts:   cfg.Parser.MaxAttempts,
		RetryBase:     cfg.Parser.RetryBase,
		RetryMax:      cfg.Parser.RetryMax,
	})
	if err != nil {
		return nil, fmt.Errorf("create parser worker: %w", err)
	}
	parserWorker.SetMetrics(metrics)
	var retention *jobs.Retention
	if cfg.Cleanup.Interval > 0 {
		retention, err = jobs.NewRetention(mailboxRepository, store, logger, jobs.RetentionOptions{
			BatchSize: cfg.Cleanup.BatchSize,
			Interval:  cfg.Cleanup.Interval,
			Timeout:   cfg.Cleanup.Timeout,
		})
		if err != nil {
			return nil, fmt.Errorf("create retention job: %w", err)
		}
		retention.SetMetrics(metrics)
	}
	lmtpReceiver := &wakingReceiver{receiver: receiver, wake: parserWorker.Notify}
	lmtpServer, err := lmtp.NewServer(lmtp.Options{
		Hostname:         cfg.LMTP.Hostname,
		MaxMessageBytes:  cfg.LMTP.MaxMessageBytes,
		MaxCommandBytes:  cfg.LMTP.MaxCommandBytes,
		MaxDataLineBytes: cfg.LMTP.MaxDataLineBytes,
		MaxRecipients:    cfg.LMTP.MaxRecipients,
		MaxSessions:      cfg.LMTP.MaxSessions,
		SessionTimeout:   cfg.LMTP.SessionTimeout,
		DeliveryTimeout:  cfg.LMTP.DeliveryTimeout,
	}, repository, lmtpReceiver, logger)
	if err != nil {
		return nil, fmt.Errorf("create LMTP server: %w", err)
	}
	lmtpServer.SetMetrics(metrics)
	httpServer := httpapi.NewServer(cfg.HTTP, logger)
	httpServer.SetMetrics(metrics.Handler(), metrics)
	httpServer.SetReadinessChecker(repository)
	httpServer.SetMailboxService(mailboxService, capabilityService)
	if len(cfg.BrowserSession.Key) != 0 {
		browserSessions, err := auth.NewBrowserSessionManager(cfg.BrowserSession.Key, cfg.BrowserSession.Lifetime)
		if err != nil {
			return nil, fmt.Errorf("create browser session manager: %w", err)
		}
		httpServer.SetBrowserSessions(browserSessions)
	}
	if cfg.Compatibility.DuckMailEnabled {
		duckMailRepository, err := postgres.NewDuckMailRepository(pool)
		if err != nil {
			return nil, fmt.Errorf("create DuckMail repository: %w", err)
		}
		duckMailService, err := duckmail.NewService(duckMailRepository, mailboxService, capabilityService, duckmail.Options{
			PublicDomains: cfg.Inbox.PublicDomains,
			DefaultTTL:    cfg.Inbox.DefaultTTL,
			MaxTTL:        cfg.Inbox.MaxTTL,
		})
		if err != nil {
			return nil, fmt.Errorf("create DuckMail service: %w", err)
		}
		httpServer.SetDuckMailService(duckMailService)
	}
	if cfg.Compatibility.YYDSEnabled {
		yydsService, err := yyds.NewService(mailboxService, capabilityService, cfg.Inbox.PublicDomains)
		if err != nil {
			return nil, fmt.Errorf("create YYDS compatibility service: %w", err)
		}
		httpServer.SetYYDSService(yydsService)
	}
	if cfg.Compatibility.CloudflareTempEnabled {
		cloudflareRepository, err := postgres.NewCloudflareTempRepository(pool)
		if err != nil {
			return nil, fmt.Errorf("create Cloudflare Temp Email repository: %w", err)
		}
		cloudflareService, err := cloudflaretemp.NewService(mailboxService, cloudflareRepository, cfg.Inbox.PublicDomains)
		if err != nil {
			return nil, fmt.Errorf("create Cloudflare Temp Email compatibility service: %w", err)
		}
		httpServer.SetCloudflareTempService(cloudflareService, cfg.Compatibility.CloudflareLegacyPathsEnabled)
	}

	cleanupPool = false
	return &App{
		config:       cfg,
		logger:       logger,
		http:         httpServer,
		lmtp:         lmtpServer,
		pool:         pool,
		repository:   repository,
		parserWorker: parserWorker,
		retention:    retention,
		mailbox:      mailboxService,
	}, nil
}

// Run starts all services, waits for cancellation or failure, and shuts down.
func (a *App) Run(ctx context.Context) (returnError error) {
	defer a.pool.Close()
	startupContext, startupCancel := context.WithTimeout(ctx, a.config.Postgres.ConnectTimeout)
	defer startupCancel()
	if err := a.repository.Ready(startupContext); err != nil {
		return fmt.Errorf("verify postgres readiness: %w", err)
	}
	serviceLease, err := postgres.AcquireServiceLease(startupContext, a.config.Postgres.DSN)
	if err != nil {
		return fmt.Errorf("acquire service maintenance lease: %w", err)
	}
	defer func() {
		releaseContext, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		if err := serviceLease.Release(releaseContext); err != nil {
			returnError = errors.Join(returnError, fmt.Errorf("release service maintenance lease: %w", err))
		}
	}()
	startupCancel()

	lmtpListener, err := net.Listen("tcp", a.config.LMTP.Addr)
	if err != nil {
		return fmt.Errorf("listen for LMTP: %w", err)
	}
	a.logger.Info("LMTP server listening", "addr", lmtpListener.Addr().String())

	serviceContext, cancelServices := context.WithCancel(ctx)
	defer cancelServices()
	serviceCount := 3
	if a.retention != nil {
		serviceCount++
	}
	results := make(chan serviceResult, serviceCount)
	go func() {
		results <- serviceResult{name: "http", err: a.http.ListenAndServe()}
	}()
	go func() {
		results <- serviceResult{name: "lmtp", err: a.lmtp.Serve(serviceContext, lmtpListener)}
	}()
	go func() {
		results <- serviceResult{name: "parser", err: a.parserWorker.Run(serviceContext)}
	}()
	if a.retention != nil {
		go func() {
			results <- serviceResult{name: "retention", err: a.retention.Run(serviceContext)}
		}()
	}
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

	for receivedResults < serviceCount {
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

type wakingReceiver struct {
	receiver *message.Receiver
	wake     func()
}

func (r *wakingReceiver) CheckCapacity(ctx context.Context) error {
	return r.receiver.CheckCapacity(ctx)
}

func (r *wakingReceiver) Receive(ctx context.Context, request message.ReceiveRequest) (message.Receipt, error) {
	receipt, err := r.receiver.Receive(ctx, request)
	if err == nil {
		r.wake()
	}
	return receipt, err
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
