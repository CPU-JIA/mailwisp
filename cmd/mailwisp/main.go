// Command mailwisp starts the MailWisp application.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"mailwisp/internal/app"
	"mailwisp/internal/config"
	"mailwisp/internal/postgres"
	"mailwisp/internal/telemetry"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	command, err := parseCommand(arguments)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	logger := telemetry.NewLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch command.role {
	case "migrate":
		if err := postgres.Migrate(ctx, cfg.Postgres.DSN); err != nil {
			return fmt.Errorf("migrate database: %w", err)
		}
		logger.Info("database migrations complete")
		return nil
	case "serve":
		application, err := app.New(ctx, cfg, logger)
		if err != nil {
			return fmt.Errorf("create application: %w", err)
		}
		if err := application.Run(ctx); err != nil {
			return fmt.Errorf("run application: %w", err)
		}
		return nil
	case "reconcile":
		if err := app.ReconcileContent(ctx, cfg, logger, command.repairOrphans); err != nil {
			return fmt.Errorf("reconcile content: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported role %q", command.role)
	}
}

type command struct {
	role          string
	repairOrphans bool
}

func parseCommand(arguments []string) (command, error) {
	if len(arguments) == 0 {
		return command{role: "serve"}, nil
	}
	if len(arguments) == 1 && (arguments[0] == "serve" || arguments[0] == "migrate" || arguments[0] == "reconcile") {
		return command{role: arguments[0]}, nil
	}
	if len(arguments) == 2 && arguments[0] == "reconcile" && arguments[1] == "--repair-orphans" {
		return command{role: "reconcile", repairOrphans: true}, nil
	}
	return command{}, errors.New("usage: mailwisp [serve|migrate|reconcile [--repair-orphans]]")
}
