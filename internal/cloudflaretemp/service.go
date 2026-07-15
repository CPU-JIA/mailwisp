// Package cloudflaretemp isolates Cloudflare Temp Email compatibility semantics
// from MailWisp's canonical domain and authentication model.
package cloudflaretemp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"mailwisp/internal/auth"
	"mailwisp/internal/mailbox"
	"mailwisp/internal/message"
)

var (
	// ErrInvalidAddressName indicates a name that cannot be represented safely.
	ErrInvalidAddressName = errors.New("invalid Cloudflare Temp Email address name")
	// ErrMessageIDNotFound indicates an unknown or foreign compatibility ID.
	ErrMessageIDNotFound = errors.New("Cloudflare Temp Email message ID not found")
)

// MailboxService exposes canonical behavior consumed by the adapter.
type MailboxService interface {
	Create(context.Context, mailbox.CreateRequest) (mailbox.CreatedInbox, error)
	Get(context.Context, message.InboxID) (mailbox.Inbox, error)
	Delete(context.Context, message.InboxID) error
	ListMessagePage(context.Context, message.InboxID, int, int) (mailbox.MessagePage, error)
	GetMessage(context.Context, message.InboxID, message.MessageID) (mailbox.MessageDetail, error)
	DeleteMessage(context.Context, message.InboxID, message.MessageID) error
	OpenSource(context.Context, message.InboxID, message.MessageID) (mailbox.RawSource, error)
}

// IDRepository persists stable positive integer IDs required by the upstream API.
type IDRepository interface {
	EnsureInboxID(context.Context, message.InboxID) (int64, error)
	EnsureMessageIDs(context.Context, message.InboxID, []message.MessageID) (map[message.MessageID]int64, error)
	FindMessageID(context.Context, message.InboxID, int64) (message.MessageID, error)
}

// CreatedAddress is one canonical Inbox projected into the compatibility contract.
type CreatedAddress struct {
	Inbox      mailbox.Inbox
	Capability auth.IssuedCapability
	AddressID  int64
}

// Service maps the high-value Cloudflare Temp Email address and mail workflow.
type Service struct {
	mailboxes MailboxService
	ids       IDRepository
	domains   []string
}

// NewService constructs the Cloudflare Temp Email compatibility boundary.
func NewService(mailboxes MailboxService, ids IDRepository, domains []string) (*Service, error) {
	if mailboxes == nil || ids == nil || len(domains) == 0 {
		return nil, errors.New("Cloudflare Temp Email mailbox service, ID repository, and domains are required")
	}
	normalized := make([]string, 0, len(domains))
	seen := make(map[string]struct{}, len(domains))
	for _, domain := range domains {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain == "" {
			return nil, errors.New("Cloudflare Temp Email domain is empty")
		}
		if _, exists := seen[domain]; exists {
			return nil, fmt.Errorf("duplicate Cloudflare Temp Email domain %q", domain)
		}
		seen[domain] = struct{}{}
		normalized = append(normalized, domain)
	}
	return &Service{mailboxes: mailboxes, ids: ids, domains: normalized}, nil
}

// Domains returns a defensive copy of configured public receiving domains.
func (s *Service) Domains() []string { return append([]string(nil), s.domains...) }

// CreateAddress creates an Inbox and assigns its stable upstream-compatible ID.
func (s *Service) CreateAddress(ctx context.Context, name, domain string) (CreatedAddress, error) {
	localPart := normalizeAddressName(name)
	if strings.TrimSpace(name) != "" && localPart == "" {
		return CreatedAddress{}, ErrInvalidAddressName
	}
	created, err := s.mailboxes.Create(ctx, mailbox.CreateRequest{Domain: domain, LocalPart: localPart})
	if err != nil {
		return CreatedAddress{}, err
	}
	addressID, err := s.ids.EnsureInboxID(ctx, created.Inbox.ID)
	if err != nil {
		mappingErr := fmt.Errorf("assign Cloudflare Temp Email Inbox ID: %w", err)
		if cleanupErr := s.mailboxes.Delete(ctx, created.Inbox.ID); cleanupErr != nil {
			return CreatedAddress{}, errors.Join(mappingErr, fmt.Errorf("compensate Inbox creation: %w", cleanupErr))
		}
		return CreatedAddress{}, mappingErr
	}
	return CreatedAddress{Inbox: created.Inbox, Capability: created.Capability, AddressID: addressID}, nil
}

// EnsureInboxID returns a stable positive compatibility ID for an Inbox.
func (s *Service) EnsureInboxID(ctx context.Context, inboxID message.InboxID) (int64, error) {
	return s.ids.EnsureInboxID(ctx, inboxID)
}

// EnsureMessageIDs returns stable positive compatibility IDs for owned messages.
func (s *Service) EnsureMessageIDs(ctx context.Context, inboxID message.InboxID, messageIDs []message.MessageID) (map[message.MessageID]int64, error) {
	return s.ids.EnsureMessageIDs(ctx, inboxID, messageIDs)
}

// FindMessageID resolves an owned compatibility ID to the canonical UUID.
func (s *Service) FindMessageID(ctx context.Context, inboxID message.InboxID, compatibilityID int64) (message.MessageID, error) {
	return s.ids.FindMessageID(ctx, inboxID, compatibilityID)
}

// Mailboxes exposes canonical operations to the HTTP presenter.
func (s *Service) Mailboxes() MailboxService { return s.mailboxes }

func normalizeAddressName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var builder strings.Builder
	builder.Grow(len(name))
	for _, character := range name {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			builder.WriteRune(character)
		}
	}
	return builder.String()
}
