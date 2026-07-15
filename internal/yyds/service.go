// Package yyds isolates YYDS Mail compatibility semantics from the canonical domain.
package yyds

import (
	"context"
	"errors"
	"strings"

	"mailwisp/internal/auth"
	"mailwisp/internal/mailbox"
	"mailwisp/internal/message"
)

var (
	// ErrInvalidRequest indicates a request outside the supported YYDS contract.
	ErrInvalidRequest = errors.New("invalid YYDS request")
	// ErrAddressMismatch hides attempts to rotate a token for another Inbox.
	ErrAddressMismatch = errors.New("YYDS address does not match token")
)

// MailboxService exposes canonical behavior consumed by the adapter.
type MailboxService interface {
	Create(context.Context, mailbox.CreateRequest) (mailbox.CreatedInbox, error)
	Get(context.Context, message.InboxID) (mailbox.Inbox, error)
	Delete(context.Context, message.InboxID) error
	ListMessagePage(context.Context, message.InboxID, int, int) (mailbox.MessagePage, error)
	GetMessage(context.Context, message.InboxID, message.MessageID) (mailbox.MessageDetail, error)
	DeleteMessage(context.Context, message.InboxID, message.MessageID) error
	MarkMessageSeen(context.Context, message.InboxID, message.MessageID) error
	OpenSource(context.Context, message.InboxID, message.MessageID) (mailbox.RawSource, error)
	OpenAttachment(context.Context, message.InboxID, message.MessageID, string) (mailbox.AttachmentSource, error)
}

// CredentialService authenticates and atomically rotates temporary tokens.
type CredentialService interface {
	Authenticate(context.Context, string, ...auth.Scope) (auth.Principal, error)
	Rotate(context.Context, string) (auth.IssuedCapability, error)
}

// CreateAccountRequest contains the supported passwordless account fields.
type CreateAccountRequest struct {
	Address   string
	LocalPart string
	Domain    string
}

// Service maps YYDS temporary Inbox semantics to canonical services.
type Service struct {
	mailboxes   MailboxService
	credentials CredentialService
	domains     []string
}

// NewService constructs the YYDS compatibility boundary.
func NewService(mailboxes MailboxService, credentials CredentialService, domains []string) (*Service, error) {
	if mailboxes == nil || credentials == nil || len(domains) == 0 {
		return nil, errors.New("YYDS mailbox, credential service, and domains are required")
	}
	normalized := make([]string, 0, len(domains))
	for _, domain := range domains {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain == "" {
			return nil, errors.New("YYDS domain is empty")
		}
		normalized = append(normalized, domain)
	}
	return &Service{mailboxes: mailboxes, credentials: credentials, domains: normalized}, nil
}

// Domains returns the configured public receiving domains.
func (s *Service) Domains() []string { return append([]string(nil), s.domains...) }

// CreateAccount creates a passwordless temporary Inbox and returns its token once.
func (s *Service) CreateAccount(ctx context.Context, request CreateAccountRequest) (mailbox.CreatedInbox, error) {
	localPart := strings.ToLower(strings.TrimSpace(request.LocalPart))
	domain := strings.ToLower(strings.TrimSpace(request.Domain))
	if address := strings.ToLower(strings.TrimSpace(request.Address)); address != "" {
		addressLocal, addressDomain, found := strings.Cut(address, "@")
		if !found {
			addressLocal = address
		} else if addressLocal == "" || addressDomain == "" || strings.Contains(addressDomain, "@") {
			return mailbox.CreatedInbox{}, ErrInvalidRequest
		}
		if (localPart != "" && localPart != addressLocal) || (found && domain != "" && domain != addressDomain) {
			return mailbox.CreatedInbox{}, ErrInvalidRequest
		}
		localPart = addressLocal
		if found {
			domain = addressDomain
		}
	}
	created, err := s.mailboxes.Create(ctx, mailbox.CreateRequest{Domain: domain, LocalPart: localPart})
	if err != nil {
		return mailbox.CreatedInbox{}, err
	}
	return created, nil
}

// RefreshToken validates address ownership and atomically rotates the temporary token.
func (s *Service) RefreshToken(ctx context.Context, plaintext, address string) (auth.IssuedCapability, mailbox.Inbox, error) {
	principal, err := s.credentials.Authenticate(ctx, plaintext, auth.ScopeInboxRead)
	if err != nil {
		return auth.IssuedCapability{}, mailbox.Inbox{}, err
	}
	inbox, err := s.mailboxes.Get(ctx, principal.InboxID)
	if err != nil {
		return auth.IssuedCapability{}, mailbox.Inbox{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(address), inbox.Address) {
		return auth.IssuedCapability{}, mailbox.Inbox{}, ErrAddressMismatch
	}
	rotated, err := s.credentials.Rotate(ctx, plaintext)
	if err != nil {
		return auth.IssuedCapability{}, mailbox.Inbox{}, err
	}
	return rotated, inbox, nil
}

// Mailboxes exposes canonical operations to the HTTP presenter.
func (s *Service) Mailboxes() MailboxService { return s.mailboxes }
