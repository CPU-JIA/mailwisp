// Package lmtp implements the bounded local delivery protocol boundary used by Postfix.
package lmtp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"mailwisp/internal/message"
)

// InboxResolver resolves canonical recipient addresses before DATA is accepted.
type InboxResolver interface {
	ResolveInboxForDelivery(context.Context, string, int64) (message.InboxID, error)
}

// MessageReceiver durably receives one accepted LMTP transaction.
type MessageReceiver interface {
	CheckCapacity(context.Context) error
	Receive(context.Context, message.ReceiveRequest) (message.Receipt, error)
}

// Metrics observes bounded LMTP admission and delivery outcomes.
type Metrics interface {
	LMTPSessionOpened()
	LMTPSessionClosed()
	LMTPSessionRejected()
	ObserveLMTPDelivery(int)
	ObserveLMTPQuotaRejected(string)
	ObserveLMTPStorageRejected(string)
}

// Options configures LMTP resource limits and deadlines.
type Options struct {
	Hostname         string
	MaxMessageBytes  int64
	MaxCommandBytes  int
	MaxDataLineBytes int
	MaxRecipients    int
	MaxSessions      int
	SessionTimeout   time.Duration
	DeliveryTimeout  time.Duration
}

// DefaultOptions returns conservative limits for a self-hosted reference deployment.
func DefaultOptions(hostname string) Options {
	return Options{
		Hostname:         hostname,
		MaxMessageBytes:  25 << 20,
		MaxCommandBytes:  4 << 10,
		MaxDataLineBytes: 64 << 10,
		MaxRecipients:    100,
		MaxSessions:      64,
		SessionTimeout:   5 * time.Minute,
		DeliveryTimeout:  30 * time.Second,
	}
}

// Server accepts bounded LMTP sessions.
type Server struct {
	options  Options
	resolver InboxResolver
	receiver MessageReceiver
	logger   *slog.Logger
	metrics  Metrics
}

// SetMetrics enables LMTP admission and delivery observations.
func (s *Server) SetMetrics(metrics Metrics) { s.metrics = metrics }

// NewServer constructs an LMTP server.
func NewServer(options Options, resolver InboxResolver, receiver MessageReceiver, logger *slog.Logger) (*Server, error) {
	if strings.TrimSpace(options.Hostname) == "" {
		return nil, errors.New("LMTP hostname is required")
	}
	if options.MaxMessageBytes <= 0 || options.MaxCommandBytes <= 0 || options.MaxDataLineBytes <= 0 {
		return nil, errors.New("LMTP byte limits must be positive")
	}
	if options.MaxRecipients <= 0 || options.MaxSessions <= 0 {
		return nil, errors.New("LMTP concurrency limits must be positive")
	}
	if options.SessionTimeout <= 0 || options.DeliveryTimeout <= 0 {
		return nil, errors.New("LMTP timeouts must be positive")
	}
	if options.SessionTimeout < options.DeliveryTimeout {
		return nil, errors.New("LMTP session timeout must not be shorter than delivery timeout")
	}
	if resolver == nil {
		return nil, errors.New("LMTP inbox resolver is required")
	}
	if receiver == nil {
		return nil, errors.New("LMTP message receiver is required")
	}
	if logger == nil {
		return nil, errors.New("LMTP logger is required")
	}
	return &Server{options: options, resolver: resolver, receiver: receiver, logger: logger}, nil
}

// Serve accepts sessions until the context is canceled or the listener fails.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	if listener == nil {
		return errors.New("LMTP listener is required")
	}
	stopClosing := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stopClosing()

	semaphore := make(chan struct{}, s.options.MaxSessions)
	var sessions sync.WaitGroup
	defer sessions.Wait()

	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept LMTP connection: %w", err)
		}

		select {
		case semaphore <- struct{}{}:
			if s.metrics != nil {
				s.metrics.LMTPSessionOpened()
			}
			sessions.Add(1)
			go func() {
				defer sessions.Done()
				defer func() { <-semaphore }()
				if s.metrics != nil {
					defer s.metrics.LMTPSessionClosed()
				}
				if err := s.serveConnection(ctx, connection); err != nil && ctx.Err() == nil {
					s.logger.Warn("LMTP session ended with error", "error", err)
				}
			}()
		default:
			if s.metrics != nil {
				s.metrics.LMTPSessionRejected()
			}
			s.logger.Warn("LMTP session rejected by concurrency limit")
			_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
			_, _ = io.WriteString(connection, "421 4.3.2 Too many LMTP sessions\r\n")
			_ = connection.Close()
		}
	}
}

func (s *Server) serveConnection(parent context.Context, connection net.Conn) error {
	defer connection.Close()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stopClosing := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stopClosing()

	session := &session{
		server:     s,
		connection: connection,
		reader:     bufio.NewReaderSize(connection, 32<<10),
		writer:     bufio.NewWriterSize(connection, 4<<10),
	}
	return session.run(ctx)
}
