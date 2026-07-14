// Package telemetry provides process-level logs, health, and metrics wiring.
package telemetry

import (
	"log/slog"
	"os"
)

// NewLogger creates the repository-standard structured JSON logger.
func NewLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}
