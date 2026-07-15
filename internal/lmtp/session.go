package lmtp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"mailwisp/internal/message"
)

type recipient struct {
	address string
	inboxID message.InboxID
}

type session struct {
	server       *Server
	connection   net.Conn
	reader       *bufio.Reader
	writer       *bufio.Writer
	greeted      bool
	mailSet      bool
	sender       string
	declaredSize int64
	recipients   []recipient
	recipientSet map[message.InboxID]struct{}
}

func (s *session) run(ctx context.Context) error {
	if err := s.reply(220, "2.0.0", s.server.options.Hostname+" MailWisp LMTP ready"); err != nil {
		return err
	}
	for {
		if err := s.connection.SetDeadline(time.Now().Add(s.server.options.SessionTimeout)); err != nil {
			return fmt.Errorf("set LMTP session deadline: %w", err)
		}
		line, err := readLimitedLine(s.reader, s.server.options.MaxCommandBytes)
		if errors.Is(err, ErrCommandLineTooLong) {
			if replyErr := s.reply(500, "5.5.2", "Command line too long"); replyErr != nil {
				return replyErr
			}
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read LMTP command: %w", err)
		}
		verb, argument := splitCommand(line)
		switch verb {
		case "LHLO":
			if strings.TrimSpace(argument) == "" {
				if err := s.reply(501, "5.5.4", "LHLO requires a hostname"); err != nil {
					return err
				}
				continue
			}
			s.resetTransaction()
			s.greeted = true
			if err := s.capabilities(); err != nil {
				return err
			}
		case "MAIL":
			if !s.greeted {
				if err := s.reply(503, "5.5.1", "Send LHLO first"); err != nil {
					return err
				}
				continue
			}
			if s.mailSet {
				if err := s.reply(503, "5.5.1", "Nested MAIL command"); err != nil {
					return err
				}
				continue
			}
			sender, declaredSize, err := parseMailFrom(argument)
			if err != nil {
				if replyErr := s.reply(501, "5.5.4", err.Error()); replyErr != nil {
					return replyErr
				}
				continue
			}
			if declaredSize > s.server.options.MaxMessageBytes {
				if err := s.reply(552, "5.3.4", "Declared message size exceeds fixed maximum"); err != nil {
					return err
				}
				continue
			}
			s.sender = sender
			s.declaredSize = declaredSize
			s.mailSet = true
			s.recipientSet = make(map[message.InboxID]struct{})
			if err := s.reply(250, "2.1.0", "Sender OK"); err != nil {
				return err
			}
		case "RCPT":
			if !s.greeted || !s.mailSet {
				if err := s.reply(503, "5.5.1", "Need MAIL before RCPT"); err != nil {
					return err
				}
				continue
			}
			address, err := parseRecipient(argument)
			if err != nil {
				if replyErr := s.reply(501, "5.5.4", err.Error()); replyErr != nil {
					return replyErr
				}
				continue
			}
			resolveContext, cancel := context.WithTimeout(ctx, s.server.options.DeliveryTimeout)
			inboxID, resolveErr := s.server.resolver.ResolveInboxForDelivery(resolveContext, address, s.declaredSize)
			cancel()
			if errors.Is(resolveErr, message.ErrInboxNotFound) {
				if err := s.reply(550, "5.1.1", "Recipient address rejected"); err != nil {
					return err
				}
				continue
			}
			if resolveErr != nil {
				if reason := quotaRejectionReason(resolveErr); reason != "" {
					if s.server.metrics != nil {
						s.server.metrics.ObserveLMTPQuotaRejected(reason)
					}
					if err := s.reply(552, "5.2.2", "Recipient Inbox quota exceeded"); err != nil {
						return err
					}
					continue
				}
				if err := s.reply(451, "4.3.0", "Temporary recipient lookup failure"); err != nil {
					return err
				}
				continue
			}
			if _, exists := s.recipientSet[inboxID]; exists {
				if err := s.reply(250, "2.1.5", "Recipient OK"); err != nil {
					return err
				}
				continue
			}
			if len(s.recipients) >= s.server.options.MaxRecipients {
				if err := s.reply(452, "4.5.3", "Too many recipients"); err != nil {
					return err
				}
				continue
			}
			s.recipientSet[inboxID] = struct{}{}
			s.recipients = append(s.recipients, recipient{address: address, inboxID: inboxID})
			if err := s.reply(250, "2.1.5", "Recipient OK"); err != nil {
				return err
			}
		case "DATA":
			if !s.mailSet || len(s.recipients) == 0 {
				if err := s.reply(503, "5.5.1", "Need MAIL and RCPT before DATA"); err != nil {
					return err
				}
				continue
			}
			if strings.TrimSpace(argument) != "" {
				if err := s.reply(501, "5.5.4", "DATA does not accept parameters"); err != nil {
					return err
				}
				continue
			}
			capacityContext, capacityCancel := context.WithTimeout(ctx, s.server.options.DeliveryTimeout)
			capacityErr := s.server.receiver.CheckCapacity(capacityContext)
			capacityCancel()
			if capacityErr != nil {
				reason := "check_error"
				code, enhanced, text := 451, "4.3.0", "Temporary storage check failure"
				if errors.Is(capacityErr, message.ErrInsufficientStorage) {
					reason = "capacity"
					code, enhanced, text = 452, "4.3.1", "Insufficient system storage"
				}
				if s.server.metrics != nil {
					s.server.metrics.ObserveLMTPStorageRejected(reason)
				}
				if err := s.reply(code, enhanced, text); err != nil {
					return err
				}
				continue
			}
			if err := s.reply(354, "2.0.0", "End data with <CR><LF>.<CR><LF>"); err != nil {
				return err
			}
			if err := s.receiveData(ctx); err != nil {
				return err
			}
		case "RSET":
			s.resetTransaction()
			if err := s.reply(250, "2.0.0", "Reset state"); err != nil {
				return err
			}
		case "NOOP":
			if err := s.reply(250, "2.0.0", "OK"); err != nil {
				return err
			}
		case "QUIT":
			return s.reply(221, "2.0.0", "Bye")
		case "HELO", "EHLO":
			if err := s.reply(500, "5.5.1", "Use LHLO for LMTP"); err != nil {
				return err
			}
		case "VRFY":
			if err := s.reply(252, "2.5.2", "Cannot verify user"); err != nil {
				return err
			}
		default:
			if err := s.reply(500, "5.5.1", "Command unrecognized"); err != nil {
				return err
			}
		}
	}
}

func (s *session) receiveData(ctx context.Context) error {
	data := newDataReader(s.reader, s.server.options.MaxMessageBytes, s.server.options.MaxDataLineBytes)
	inboxIDs := make([]message.InboxID, len(s.recipients))
	for index, recipient := range s.recipients {
		inboxIDs[index] = recipient.inboxID
	}
	deliveryContext, cancel := context.WithTimeout(ctx, s.server.options.DeliveryTimeout)
	_, receiveErr := s.server.receiver.Receive(deliveryContext, message.ReceiveRequest{
		EnvelopeSender: s.sender,
		Recipients:     inboxIDs,
		Raw:            data,
	})
	cancel()

	if !data.ended {
		if drainErr := data.drain(); drainErr != nil {
			return fmt.Errorf("drain rejected LMTP DATA: %w", drainErr)
		}
	}

	code, enhanced, text := classifyDeliveryError(receiveErr)
	if s.server.metrics != nil {
		s.server.metrics.ObserveLMTPDelivery(code)
		if reason := quotaRejectionReason(receiveErr); reason != "" {
			s.server.metrics.ObserveLMTPQuotaRejected(reason)
		}
		if errors.Is(receiveErr, message.ErrInsufficientStorage) {
			s.server.metrics.ObserveLMTPStorageRejected("capacity")
		}
	}
	for _, recipient := range s.recipients {
		if err := s.reply(code, enhanced, recipient.address+" "+text); err != nil {
			return err
		}
	}
	s.resetTransaction()
	return nil
}

func classifyDeliveryError(err error) (int, string, string) {
	switch {
	case err == nil:
		return 250, "2.1.5", "delivered"
	case errors.Is(err, ErrDataTooLarge), errors.Is(err, message.ErrContentTooLarge):
		return 552, "5.3.4", "message too large"
	case errors.Is(err, ErrDataLineTooLong):
		return 554, "5.6.0", "message line too long"
	case errors.Is(err, message.ErrInboxNotFound):
		return 550, "5.1.1", "recipient no longer available"
	case errors.Is(err, message.ErrInboxMessageQuotaExceeded):
		return 552, "5.2.2", "recipient message quota exceeded"
	case errors.Is(err, message.ErrInboxStorageQuotaExceeded):
		return 552, "5.2.2", "recipient storage quota exceeded"
	case errors.Is(err, message.ErrInsufficientStorage):
		return 452, "4.3.1", "insufficient system storage"
	case errors.Is(err, message.ErrInvalidDelivery):
		return 554, "5.6.0", "invalid delivery"
	default:
		return 451, "4.3.0", "temporary delivery failure"
	}
}

func quotaRejectionReason(err error) string {
	switch {
	case errors.Is(err, message.ErrInboxMessageQuotaExceeded):
		return "messages"
	case errors.Is(err, message.ErrInboxStorageQuotaExceeded):
		return "storage_bytes"
	default:
		return ""
	}
}

func (s *session) capabilities() error {
	lines := []string{
		s.server.options.Hostname,
		"8BITMIME",
		"PIPELINING",
		fmt.Sprintf("SIZE %d", s.server.options.MaxMessageBytes),
		"ENHANCEDSTATUSCODES",
	}
	for index, line := range lines {
		separator := "-"
		if index == len(lines)-1 {
			separator = " "
		}
		if _, err := fmt.Fprintf(s.writer, "250%s%s\r\n", separator, line); err != nil {
			return fmt.Errorf("write LMTP capability: %w", err)
		}
	}
	if err := s.writer.Flush(); err != nil {
		return fmt.Errorf("flush LMTP capabilities: %w", err)
	}
	return nil
}

func (s *session) reply(code int, enhanced, text string) error {
	if _, err := fmt.Fprintf(s.writer, "%d %s %s\r\n", code, enhanced, text); err != nil {
		return fmt.Errorf("write LMTP response: %w", err)
	}
	if err := s.writer.Flush(); err != nil {
		return fmt.Errorf("flush LMTP response: %w", err)
	}
	return nil
}

func (s *session) resetTransaction() {
	s.mailSet = false
	s.sender = ""
	s.declaredSize = 0
	s.recipients = nil
	s.recipientSet = nil
}

func splitCommand(line string) (string, string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", ""
	}
	verb, argument, found := strings.Cut(line, " ")
	if !found {
		return strings.ToUpper(verb), ""
	}
	return strings.ToUpper(verb), strings.TrimSpace(argument)
}

func parseMailFrom(argument string) (string, int64, error) {
	address, parameters, err := parsePath(argument, "FROM:", true)
	if err != nil {
		return "", 0, err
	}
	var declaredSize int64
	var hasSize, hasBody bool
	for _, parameter := range parameters {
		name, value, hasValue := strings.Cut(parameter, "=")
		switch strings.ToUpper(name) {
		case "SIZE":
			if hasSize {
				return "", 0, errors.New("SIZE must not be repeated")
			}
			hasSize = true
			if !hasValue {
				return "", 0, errors.New("SIZE requires a value")
			}
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil || parsed < 0 {
				return "", 0, errors.New("SIZE must be a non-negative integer")
			}
			declaredSize = parsed
		case "BODY":
			if hasBody {
				return "", 0, errors.New("BODY must not be repeated")
			}
			hasBody = true
			if !hasValue || (strings.ToUpper(value) != "7BIT" && strings.ToUpper(value) != "8BITMIME") {
				return "", 0, errors.New("unsupported BODY parameter")
			}
		default:
			return "", 0, errors.New("unsupported MAIL parameter")
		}
	}
	return address, declaredSize, nil
}

func parseRecipient(argument string) (string, error) {
	address, parameters, err := parsePath(argument, "TO:", false)
	if err != nil {
		return "", err
	}
	if len(parameters) != 0 {
		return "", errors.New("unsupported RCPT parameter")
	}
	return address, nil
}

func parsePath(argument, prefix string, allowEmpty bool) (string, []string, error) {
	if len(argument) < len(prefix) || !strings.EqualFold(argument[:len(prefix)], prefix) {
		return "", nil, fmt.Errorf("expected %s<address>", prefix)
	}
	remainder := strings.TrimSpace(argument[len(prefix):])
	if !strings.HasPrefix(remainder, "<") {
		return "", nil, errors.New("path must start with <")
	}
	closing := strings.IndexByte(remainder, '>')
	if closing < 0 {
		return "", nil, errors.New("path must end with >")
	}
	rawAddress := remainder[1:closing]
	address, err := canonicalAddress(rawAddress, allowEmpty)
	if err != nil {
		return "", nil, err
	}
	parameters := strings.Fields(strings.TrimSpace(remainder[closing+1:]))
	return address, parameters, nil
}

func canonicalAddress(address string, allowEmpty bool) (string, error) {
	if address == "" && allowEmpty {
		return "", nil
	}
	if len(address) < 3 || len(address) > 320 || strings.Count(address, "@") != 1 {
		return "", errors.New("invalid envelope address")
	}
	local, domain, _ := strings.Cut(address, "@")
	if local == "" || domain == "" || strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
		return "", errors.New("invalid envelope address")
	}
	for _, character := range address {
		if character <= 32 || character >= 127 || character == '<' || character == '>' {
			return "", errors.New("SMTPUTF8 and control characters are not supported")
		}
	}
	return strings.ToLower(address), nil
}
