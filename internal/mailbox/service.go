package mailbox

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"mailwisp/internal/auth"
	"mailwisp/internal/mail"
	"mailwisp/internal/message"
)

const maxAddressGenerationAttempts = 5

// Repository persists Inbox lifecycle and owned message queries.
type Repository interface {
	CreateInbox(context.Context, NewInbox) (Inbox, error)
	GetInbox(context.Context, message.InboxID) (Inbox, error)
	DeleteInbox(context.Context, message.InboxID) ([]message.ContentRef, error)
	PurgeInbox(context.Context, message.InboxID) error
	ListMessages(context.Context, message.InboxID, Page) (MessagePage, error)
	GetMessage(context.Context, message.InboxID, message.MessageID) (MessageDetail, error)
	DeleteMessage(context.Context, message.InboxID, message.MessageID) (*message.ContentRef, error)
	MarkMessageSeen(context.Context, message.InboxID, message.MessageID, time.Time) error
	GetMessageContent(context.Context, message.InboxID, message.MessageID) (message.ContentRef, error)
}

// CapabilityIssuer creates one-time plaintext Inbox credentials.
type CapabilityIssuer interface {
	Issue(context.Context, message.InboxID, auth.ScopeSet, time.Time) (auth.IssuedCapability, error)
}

// ContentDeleter removes unreferenced immutable Raw MIME.
type ContentDeleter interface {
	Delete(message.ContentRef) error
	OpenRaw(context.Context, message.ContentRef) (io.ReadCloser, error)
}

// Options controls anonymous Inbox creation.
type Options struct {
	PublicDomains    []string
	DefaultTTL       time.Duration
	MaxTTL           time.Duration
	AttachmentParser *mail.Parser
}

// CreateRequest selects an allowlisted domain and optional lifetime.
type CreateRequest struct {
	Domain   string
	Lifetime time.Duration
}

// CreatedInbox returns plaintext capability material exactly once.
type CreatedInbox struct {
	Inbox      Inbox
	Capability auth.IssuedCapability
}

// Service implements the canonical anonymous Inbox workflow.
type Service struct {
	repository Repository
	issuer     CapabilityIssuer
	content    ContentDeleter
	parser     *mail.Parser
	domains    []string
	allowed    map[string]struct{}
	defaultTTL time.Duration
	maxTTL     time.Duration
	now        func() time.Time
	random     io.Reader
}

// NewService constructs an Inbox service with explicit lifecycle limits.
func NewService(repository Repository, issuer CapabilityIssuer, content ContentDeleter, options Options) (*Service, error) {
	if repository == nil || issuer == nil || content == nil {
		return nil, errors.New("mailbox repository, capability issuer, and content deleter are required")
	}
	if len(options.PublicDomains) == 0 {
		return nil, errors.New("at least one public Inbox domain is required")
	}
	if options.DefaultTTL <= 0 || options.MaxTTL <= 0 || options.DefaultTTL > options.MaxTTL {
		return nil, errors.New("Inbox TTL range is invalid")
	}
	domains := make([]string, 0, len(options.PublicDomains))
	allowed := make(map[string]struct{}, len(options.PublicDomains))
	for _, domain := range options.PublicDomains {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain == "" {
			return nil, errors.New("public Inbox domain is empty")
		}
		if _, exists := allowed[domain]; exists {
			return nil, fmt.Errorf("duplicate public Inbox domain %q", domain)
		}
		allowed[domain] = struct{}{}
		domains = append(domains, domain)
	}
	return &Service{
		repository: repository,
		issuer:     issuer,
		content:    content,
		parser:     options.AttachmentParser,
		domains:    domains,
		allowed:    allowed,
		defaultTTL: options.DefaultTTL,
		maxTTL:     options.MaxTTL,
		now:        time.Now,
		random:     rand.Reader,
	}, nil
}

// Create creates an Inbox and issues its full owner capability.
func (s *Service) Create(ctx context.Context, request CreateRequest) (CreatedInbox, error) {
	domain := strings.ToLower(strings.TrimSpace(request.Domain))
	if domain == "" {
		domain = s.domains[0]
	}
	if _, exists := s.allowed[domain]; !exists {
		return CreatedInbox{}, ErrInvalidDomain
	}
	lifetime := request.Lifetime
	if lifetime == 0 {
		lifetime = s.defaultTTL
	}
	if lifetime <= 0 || lifetime > s.maxTTL {
		return CreatedInbox{}, ErrInvalidLifetime
	}
	now := s.now().UTC()
	expiresAt := now.Add(lifetime)
	var inbox Inbox
	var err error
	for range maxAddressGenerationAttempts {
		localPart, generationErr := generateLocalPart(s.random)
		if generationErr != nil {
			return CreatedInbox{}, fmt.Errorf("generate Inbox address: %w", generationErr)
		}
		inbox, err = s.repository.CreateInbox(ctx, NewInbox{
			Address: localPart + "@" + domain, ExpiresAt: expiresAt, CreatedAt: now,
		})
		if errors.Is(err, ErrAddressConflict) {
			continue
		}
		if err != nil {
			return CreatedInbox{}, fmt.Errorf("create Inbox: %w", err)
		}
		break
	}
	if errors.Is(err, ErrAddressConflict) || inbox.ID == "" {
		return CreatedInbox{}, ErrAddressConflict
	}
	scopes, err := auth.NewScopeSet(
		auth.ScopeInboxRead,
		auth.ScopeInboxDelete,
		auth.ScopeMessageRead,
		auth.ScopeMessageDelete,
	)
	if err != nil {
		return CreatedInbox{}, err
	}
	capability, err := s.issuer.Issue(ctx, inbox.ID, scopes, expiresAt)
	if err != nil {
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		cleanupErr := s.repository.PurgeInbox(cleanupContext, inbox.ID)
		return CreatedInbox{}, errors.Join(fmt.Errorf("issue Inbox capability: %w", err), cleanupErr)
	}
	return CreatedInbox{Inbox: inbox, Capability: capability}, nil
}

// Get returns one active Inbox owned by the authenticated principal.
func (s *Service) Get(ctx context.Context, inboxID message.InboxID) (Inbox, error) {
	return s.repository.GetInbox(ctx, inboxID)
}

// Delete permanently removes an Inbox and any newly unreferenced Raw MIME.
func (s *Service) Delete(ctx context.Context, inboxID message.InboxID) error {
	refs, err := s.repository.DeleteInbox(ctx, inboxID)
	if err != nil {
		return err
	}
	return s.deleteContent(refs)
}

// ListMessages returns a bounded newest-first Inbox page.
func (s *Service) ListMessages(ctx context.Context, inboxID message.InboxID, limit int) ([]MessageSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	page, err := s.repository.ListMessages(ctx, inboxID, Page{Limit: limit})
	return page.Items, err
}

// ListMessagePage returns a compatibility-oriented offset page.
func (s *Service) ListMessagePage(ctx context.Context, inboxID message.InboxID, limit, offset int) (MessagePage, error) {
	if limit <= 0 || limit > 100 || offset < 0 {
		return MessagePage{}, errors.New("message page is invalid")
	}
	return s.repository.ListMessages(ctx, inboxID, Page{Limit: limit, Offset: offset})
}

// GetMessage returns one owned message.
func (s *Service) GetMessage(ctx context.Context, inboxID message.InboxID, messageID message.MessageID) (MessageDetail, error) {
	return s.repository.GetMessage(ctx, inboxID, messageID)
}

// DeleteMessage removes one owned message and newly unreferenced Raw MIME.
func (s *Service) DeleteMessage(ctx context.Context, inboxID message.InboxID, messageID message.MessageID) error {
	ref, err := s.repository.DeleteMessage(ctx, inboxID, messageID)
	if err != nil {
		return err
	}
	if ref == nil {
		return nil
	}
	return s.content.Delete(*ref)
}

// MarkMessageSeen records that one owned message has been opened.
func (s *Service) MarkMessageSeen(ctx context.Context, inboxID message.InboxID, messageID message.MessageID) error {
	return s.repository.MarkMessageSeen(ctx, inboxID, messageID, s.now().UTC())
}

// OpenSource opens one owned immutable RFC 822 source stream.
func (s *Service) OpenSource(ctx context.Context, inboxID message.InboxID, messageID message.MessageID) (RawSource, error) {
	ref, err := s.repository.GetMessageContent(ctx, inboxID, messageID)
	if err != nil {
		return RawSource{}, err
	}
	reader, err := s.content.OpenRaw(ctx, ref)
	if err != nil {
		return RawSource{}, fmt.Errorf("open Raw MIME: %w", err)
	}
	return RawSource{Reader: reader, Size: ref.SizeBytes}, nil
}

// OpenAttachment opens one owned decoded MIME attachment by PartPath.
func (s *Service) OpenAttachment(ctx context.Context, inboxID message.InboxID, messageID message.MessageID, partPath string) (AttachmentSource, error) {
	if s.parser == nil {
		return AttachmentSource{}, errors.New("attachment parser is not configured")
	}
	detail, err := s.repository.GetMessage(ctx, inboxID, messageID)
	if err != nil {
		return AttachmentSource{}, err
	}
	var selected *mail.Attachment
	for index := range detail.Attachments {
		if detail.Attachments[index].PartPath == partPath {
			selected = &detail.Attachments[index]
			break
		}
	}
	if selected == nil {
		return AttachmentSource{}, ErrMessageNotFound
	}
	ref, err := s.repository.GetMessageContent(ctx, inboxID, messageID)
	if err != nil {
		return AttachmentSource{}, err
	}
	raw, err := s.content.OpenRaw(ctx, ref)
	if err != nil {
		return AttachmentSource{}, fmt.Errorf("open Raw MIME: %w", err)
	}
	stream, err := s.parser.OpenAttachment(ctx, raw, selected.PartPath)
	if err != nil {
		return AttachmentSource{}, err
	}
	return AttachmentSource{Reader: stream, FileName: selected.FileName, ContentType: selected.ContentType, Size: selected.SizeBytes}, nil
}

func (s *Service) deleteContent(refs []message.ContentRef) error {
	var errs []error
	for _, ref := range refs {
		if err := s.content.Delete(ref); err != nil {
			errs = append(errs, fmt.Errorf("delete Raw MIME %q: %w", ref.Key, err))
		}
	}
	return errors.Join(errs...)
}

func generateLocalPart(random io.Reader) (string, error) {
	var entropy [12]byte
	if _, err := io.ReadFull(random, entropy[:]); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(entropy[:])), nil
}
