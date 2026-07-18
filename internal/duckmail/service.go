package duckmail

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"mailwisp/internal/auth"
	"mailwisp/internal/mailbox"
	"mailwisp/internal/message"
)

var (
	// ErrInvalidAccount indicates a DuckMail request outside the supported contract.
	ErrInvalidAccount = errors.New("invalid DuckMail account")
	// ErrAccountConflict indicates an existing requested address.
	ErrAccountConflict = errors.New("DuckMail account already exists")
	// ErrLoginFailed intentionally hides account existence and lifecycle state.
	ErrLoginFailed = errors.New("DuckMail login failed")
	// ErrPermanentUnsupported rejects DuckMail's permanent account extension.
	ErrPermanentUnsupported = errors.New("permanent DuckMail accounts are unsupported")
)

// NewAccount contains a compatibility password credential and Inbox state.
type NewAccount struct {
	Address      string
	PasswordHash string
	CreatedAt    time.Time
	ExpiresAt    time.Time
}

// Account contains the compatibility credential and canonical Inbox.
type Account struct {
	Inbox        mailbox.Inbox
	PasswordHash string
}

// Repository persists DuckMail-only password credentials.
type Repository interface {
	CreateAccount(context.Context, NewAccount) (mailbox.Inbox, error)
	FindAccountByAddress(context.Context, string) (Account, error)
}

// MailboxService exposes canonical operations consumed by the adapter.
type MailboxService interface {
	Get(context.Context, message.InboxID) (mailbox.Inbox, error)
	Delete(context.Context, message.InboxID) error
	ListMessagePage(context.Context, message.InboxID, int, int) (mailbox.MessagePage, error)
	GetMessage(context.Context, message.InboxID, message.MessageID) (mailbox.MessageDetail, error)
	DeleteMessage(context.Context, message.InboxID, message.MessageID) error
	MarkMessageSeen(context.Context, message.InboxID, message.MessageID) error
	OpenSource(context.Context, message.InboxID, message.MessageID) (mailbox.RawSource, error)
}

// CreateAccountRequest is the DuckMail account request semantics.
type CreateAccountRequest struct {
	Address   string
	Password  string
	ExpiresIn *int64
}

// Options controls the compatibility boundary.
type Options struct {
	PublicDomains []string
	DefaultTTL    time.Duration
	MaxTTL        time.Duration
}

// Service isolates DuckMail password-account semantics from canonical MailWisp.
type Service struct {
	repository Repository
	mailboxes  MailboxService
	issuer     CapabilityIssuer
	domains    []string
	allowed    map[string]struct{}
	defaultTTL time.Duration
	maxTTL     time.Duration
	dummyHash  string
	now        func() time.Time
	kdfSlots   chan struct{}
}

// CapabilityIssuer issues a canonical Bearer accepted by the adapter.
type CapabilityIssuer interface {
	Issue(context.Context, message.InboxID, auth.ScopeSet, time.Time) (auth.IssuedCapability, error)
}

// NewService constructs a DuckMail compatibility service.
func NewService(repository Repository, mailboxes MailboxService, issuer CapabilityIssuer, options Options) (*Service, error) {
	if repository == nil || mailboxes == nil || issuer == nil {
		return nil, errors.New("DuckMail repository, mailbox service, and capability issuer are required")
	}
	if len(options.PublicDomains) == 0 || options.DefaultTTL <= 0 || options.MaxTTL < options.DefaultTTL {
		return nil, errors.New("DuckMail options are invalid")
	}
	dummyHash, err := hashPassword("mailwisp-dummy-password")
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]struct{}, len(options.PublicDomains))
	domains := make([]string, 0, len(options.PublicDomains))
	for _, domain := range options.PublicDomains {
		domain = strings.ToLower(strings.TrimSpace(domain))
		allowed[domain] = struct{}{}
		domains = append(domains, domain)
	}
	return &Service{repository: repository, mailboxes: mailboxes, issuer: issuer, domains: domains, allowed: allowed, defaultTTL: options.DefaultTTL, maxTTL: options.MaxTTL, dummyHash: dummyHash, now: time.Now, kdfSlots: make(chan struct{}, 2)}, nil
}

// Domains returns the configured verified public compatibility domains.
func (s *Service) Domains() []string { return append([]string(nil), s.domains...) }

// CreateAccount creates an exact requested address with an Argon2id credential.
func (s *Service) CreateAccount(ctx context.Context, request CreateAccountRequest) (mailbox.Inbox, error) {
	if ctx == nil {
		return mailbox.Inbox{}, errors.New("DuckMail account context is required")
	}
	address, err := s.validateAddress(request.Address)
	if err != nil || len(request.Password) < 6 || len(request.Password) > 128 {
		return mailbox.Inbox{}, ErrInvalidAccount
	}
	lifetime := s.defaultTTL
	if request.ExpiresIn != nil {
		if *request.ExpiresIn == 0 || *request.ExpiresIn == -1 {
			return mailbox.Inbox{}, ErrPermanentUnsupported
		}
		if *request.ExpiresIn < 0 || *request.ExpiresIn > int64(s.maxTTL/time.Second) {
			return mailbox.Inbox{}, ErrInvalidAccount
		}
		lifetime = time.Duration(*request.ExpiresIn) * time.Second
	}
	var hash string
	err = s.withKDF(ctx, func() error {
		var hashErr error
		hash, hashErr = hashPassword(request.Password)
		return hashErr
	})
	if err != nil {
		return mailbox.Inbox{}, err
	}
	now := s.now().UTC()
	inbox, err := s.repository.CreateAccount(ctx, NewAccount{Address: address, PasswordHash: hash, CreatedAt: now, ExpiresAt: now.Add(lifetime)})
	if err != nil {
		return mailbox.Inbox{}, err
	}
	return inbox, nil
}

// Login validates Address/Password and returns a canonical opaque Bearer.
func (s *Service) Login(ctx context.Context, address, password string) (auth.IssuedCapability, error) {
	if ctx == nil {
		return auth.IssuedCapability{}, errors.New("DuckMail login context is required")
	}
	normalized := strings.ToLower(strings.TrimSpace(address))
	account, err := s.repository.FindAccountByAddress(ctx, normalized)
	if err != nil {
		if kdfErr := s.withKDF(ctx, func() error { verifyPassword(s.dummyHash, password); return nil }); kdfErr != nil {
			return auth.IssuedCapability{}, kdfErr
		}
		if errors.Is(err, ErrLoginFailed) {
			return auth.IssuedCapability{}, ErrLoginFailed
		}
		return auth.IssuedCapability{}, err
	}
	verified := false
	if err := s.withKDF(ctx, func() error { verified = verifyPassword(account.PasswordHash, password); return nil }); err != nil {
		return auth.IssuedCapability{}, err
	}
	if !verified {
		return auth.IssuedCapability{}, ErrLoginFailed
	}
	scopes, err := auth.NewScopeSet(auth.ScopeInboxRead, auth.ScopeInboxDelete, auth.ScopeMessageRead, auth.ScopeMessageUpdate, auth.ScopeMessageDelete)
	if err != nil {
		return auth.IssuedCapability{}, err
	}
	issued, err := s.issuer.Issue(ctx, account.Inbox.ID, scopes, account.Inbox.ExpiresAt)
	if err != nil {
		return auth.IssuedCapability{}, fmt.Errorf("issue DuckMail compatibility token: %w", err)
	}
	return issued, nil
}

// Mailboxes exposes canonical Inbox operations to the HTTP presenter.
func (s *Service) Mailboxes() MailboxService { return s.mailboxes }

func (s *Service) validateAddress(raw string) (string, error) {
	address := strings.ToLower(strings.TrimSpace(raw))
	local, domain, found := strings.Cut(address, "@")
	if !found || len(local) < 3 || !message.ValidInboxLocalPart(local) {
		return "", ErrInvalidAccount
	}
	if _, allowed := s.allowed[domain]; !allowed {
		return "", ErrInvalidAccount
	}
	return address, nil
}

func (s *Service) withKDF(ctx context.Context, operation func() error) error {
	select {
	case s.kdfSlots <- struct{}{}:
		defer func() { <-s.kdfSlots }()
		return operation()
	case <-ctx.Done():
		return ctx.Err()
	}
}
