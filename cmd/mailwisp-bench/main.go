// Command mailwisp-bench runs bounded black-box capacity scenarios.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mailwisp/internal/bench"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("mailwisp-bench", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var options bench.Options
	flags.StringVar(&options.Scenario, "scenario", "", "benchmark scenario")
	flags.StringVar(&options.BaseURL, "base-url", "http://127.0.0.1:18080", "MailWisp HTTP base URL")
	flags.StringVar(&options.LMTPAddress, "lmtp-address", "127.0.0.1:25250", "MailWisp LMTP address")
	flags.StringVar(&options.Domain, "domain", "example.com", "public Inbox domain")
	flags.IntVar(&options.Requests, "requests", 1000, "total bounded operations")
	flags.IntVar(&options.Concurrency, "concurrency", 16, "parallel workers")
	flags.IntVar(&options.PayloadBytes, "payload-bytes", 8192, "LMTP Raw MIME bytes")
	flags.DurationVar(&options.Timeout, "timeout", 5*time.Minute, "whole-run timeout")
	if err := flags.Parse(arguments); err != nil {
		return usageError(err)
	}
	if flags.NArg() != 0 {
		return usageError(errors.New("unexpected positional arguments"))
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	report, err := bench.Run(ctx, options)
	if err != nil {
		return fmt.Errorf("run benchmark: %w", err)
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("encode benchmark report: %w", err)
	}
	if report.Failed != 0 {
		return fmt.Errorf("benchmark completed with %d failed operations", report.Failed)
	}
	return nil
}

func usageError(err error) error {
	return fmt.Errorf("%w; usage: mailwisp-bench -scenario <%s|%s|%s> [-requests N] [-concurrency N] [-payload-bytes N]", err, bench.ScenarioHTTPRead, bench.ScenarioHTTPCreate, bench.ScenarioLMTPDelivery)
}
