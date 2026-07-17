// Command mailwisp starts the MailWisp application.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"mailwisp/internal/app"
	"mailwisp/internal/buildinfo"
	"mailwisp/internal/config"
	"mailwisp/internal/postgres"
	"mailwisp/internal/telemetry"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string, stdout io.Writer) error {
	command, err := parseCommand(arguments)
	if err != nil {
		return err
	}
	if command.role == "version" {
		return writeVersion(stdout, command.asJSON)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if command.role == "backup-verify" {
		logger := telemetry.NewLogger(slog.LevelInfo)
		if _, err := app.VerifyBackup(ctx, logger, command.path); err != nil {
			return err
		}
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	logger := telemetry.NewLogger(cfg.LogLevel)
	slog.SetDefault(logger)

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
	case "backup":
		if _, err := app.CreateBackup(ctx, cfg, logger, command.path); err != nil {
			return err
		}
		return nil
	case "restore":
		if _, err := app.RestoreBackup(ctx, cfg, logger, command.path); err != nil {
			return err
		}
		return nil
	case "cleanup":
		if _, err := app.CleanupExpired(ctx, cfg, logger); err != nil {
			return fmt.Errorf("cleanup expired Inboxes: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported role %q", command.role)
	}
}

type command struct {
	role          string
	repairOrphans bool
	asJSON        bool
	path          string
}

func parseCommand(arguments []string) (command, error) {
	if len(arguments) == 0 {
		return command{role: "serve"}, nil
	}
	if len(arguments) == 1 && (arguments[0] == "serve" || arguments[0] == "migrate" || arguments[0] == "reconcile" || arguments[0] == "cleanup") {
		return command{role: arguments[0]}, nil
	}
	if len(arguments) == 1 && arguments[0] == "version" {
		return command{role: "version"}, nil
	}
	if len(arguments) == 2 && arguments[0] == "version" && arguments[1] == "--json" {
		return command{role: "version", asJSON: true}, nil
	}
	if len(arguments) == 2 && arguments[0] == "reconcile" && arguments[1] == "--repair-orphans" {
		return command{role: "reconcile", repairOrphans: true}, nil
	}
	if len(arguments) == 2 && (arguments[0] == "backup" || arguments[0] == "restore") && arguments[1] != "" && !(arguments[0] == "backup" && arguments[1] == "verify") {
		return command{role: arguments[0], path: arguments[1]}, nil
	}
	if len(arguments) == 3 && arguments[0] == "backup" && arguments[1] == "verify" && arguments[2] != "" {
		return command{role: "backup-verify", path: arguments[2]}, nil
	}
	return command{}, errors.New("usage: mailwisp [serve|migrate|cleanup|reconcile [--repair-orphans]|backup <directory>|backup verify <bundle-directory>|restore <bundle-directory>|version [--json]]")
}

func writeVersion(output io.Writer, asJSON bool) error {
	info := buildinfo.Current()
	if asJSON {
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(info); err != nil {
			return fmt.Errorf("write version: %w", err)
		}
		return nil
	}
	if _, err := fmt.Fprintf(output, "%s %s (commit %s, built %s)\n", info.Name, info.Version, info.Commit, info.BuildDate); err != nil {
		return fmt.Errorf("write version: %w", err)
	}
	return nil
}
