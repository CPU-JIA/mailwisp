package mailbox

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"mailwisp/internal/auth"
	"mailwisp/internal/mail"
	"mailwisp/internal/message"
)

func TestCreateIssuesOwnerCapabilityAndCleansUpOnFailure(t *testing.T) {
	repository := &repositoryStub{}
	issuer := &issuerStub{}
	service, err := NewService(repository, issuer, &contentStub{}, Options{
		PublicDomains: []string{"mailwisp.test"}, DefaultTTL: time.Hour, MaxTTL: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	service.now = func() time.Time { return time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC) }
	created, err := service.Create(context.Background(), CreateRequest{})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !strings.HasSuffix(created.Inbox.Address, "@mailwisp.test") || created.Capability.Plaintext == "" {
		t.Fatalf("created = %+v", created)
	}
	if !created.Capability.Scopes.Has(auth.ScopeInboxRead, auth.ScopeInboxDelete, auth.ScopeMessageRead, auth.ScopeMessageUpdate, auth.ScopeMessageDelete) {
		t.Fatalf("scopes = %v", created.Capability.Scopes.Names())
	}

	issuer.err = errors.New("issuer unavailable")
	if _, err := service.Create(context.Background(), CreateRequest{}); err == nil || repository.purged == "" {
		t.Fatalf("Create(failed issuer) error = %v, purge = %q", err, repository.purged)
	}
}

func TestCreateSupportsValidatedExactLocalPart(t *testing.T) {
	repository := &repositoryStub{}
	service, err := NewService(repository, &issuerStub{}, &contentStub{}, Options{
		PublicDomains: []string{"mailwisp.test"}, DefaultTTL: time.Hour, MaxTTL: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), CreateRequest{LocalPart: "exact_name"})
	if err != nil || created.Inbox.Address != "exact_name@mailwisp.test" {
		t.Fatalf("Create(exact) = %+v, error = %v", created, err)
	}
	if _, err := service.Create(context.Background(), CreateRequest{LocalPart: "-invalid"}); !errors.Is(err, ErrInvalidLocalPart) {
		t.Fatalf("Create(invalid local part) error = %v", err)
	}
}

func TestCreateRejectsUnknownDomainAndLifetime(t *testing.T) {
	service, err := NewService(&repositoryStub{}, &issuerStub{}, &contentStub{}, Options{
		PublicDomains: []string{"mailwisp.test"}, DefaultTTL: time.Hour, MaxTTL: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(context.Background(), CreateRequest{Domain: "evil.test"}); !errors.Is(err, ErrInvalidDomain) {
		t.Fatalf("unknown domain error = %v", err)
	}
	if _, err := service.Create(context.Background(), CreateRequest{Lifetime: 25 * time.Hour}); !errors.Is(err, ErrInvalidLifetime) {
		t.Fatalf("long lifetime error = %v", err)
	}
}

func TestOpenAttachmentValidatesMetadataAndStreamsRawPart(t *testing.T) {
	parser, err := mail.NewParser(mail.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	raw := "Content-Type: multipart/mixed; boundary=x\n\n--x\nContent-Type: text/plain\n\nbody\n--x\nContent-Type: application/octet-stream\nContent-Disposition: attachment; filename=report.txt\n\nattachment\n--x--\n"
	repository := &repositoryStub{detail: MessageDetail{Attachments: []mail.Attachment{{PartPath: "2", FileName: "report.txt", ContentType: "application/octet-stream", SizeBytes: 10}}}}
	service, err := NewService(repository, &issuerStub{}, &contentStub{raw: raw}, Options{
		PublicDomains: []string{"mailwisp.test"}, DefaultTTL: time.Hour, MaxTTL: 24 * time.Hour, AttachmentParser: parser,
	})
	if err != nil {
		t.Fatal(err)
	}
	source, err := service.OpenAttachment(context.Background(), "018f26e5-8f04-7b44-8ba2-4a8f434dcb12", "018f26e5-8f04-7b44-8ba2-4a8f434dcb13", "2")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Reader.Close()
	content, err := io.ReadAll(source.Reader)
	if err != nil || string(content) != "attachment" {
		t.Fatalf("attachment = %q, error = %v", content, err)
	}
}

type repositoryStub struct {
	created mailboxInbox
	purged  string
	detail  MessageDetail
}

type mailboxInbox = Inbox

func (r *repositoryStub) CreateInbox(_ context.Context, candidate NewInbox) (Inbox, error) {
	r.created = Inbox{ID: message.InboxID("018f26e5-8f04-7b44-8ba2-4a8f434dcb12"), Address: candidate.Address, Status: "active", ExpiresAt: candidate.ExpiresAt, CreatedAt: candidate.CreatedAt}
	return r.created, nil
}
func (r *repositoryStub) GetInbox(context.Context, message.InboxID) (Inbox, error) {
	return r.created, nil
}
func (r *repositoryStub) DeleteInbox(context.Context, message.InboxID) ([]message.ContentRef, error) {
	return nil, nil
}
func (r *repositoryStub) PurgeInbox(_ context.Context, id message.InboxID) error {
	r.purged = string(id)
	return nil
}
func (r *repositoryStub) ListMessages(context.Context, message.InboxID, Page) (MessagePage, error) {
	return MessagePage{}, nil
}
func (r *repositoryStub) GetMessage(context.Context, message.InboxID, message.MessageID) (MessageDetail, error) {
	return r.detail, nil
}
func (r *repositoryStub) DeleteMessage(context.Context, message.InboxID, message.MessageID) (*message.ContentRef, error) {
	return nil, nil
}
func (r *repositoryStub) MarkMessageSeen(context.Context, message.InboxID, message.MessageID, time.Time) error {
	return nil
}
func (r *repositoryStub) GetMessageContent(context.Context, message.InboxID, message.MessageID) (message.ContentRef, error) {
	return message.ContentRef{}, nil
}

type issuerStub struct{ err error }

func (s *issuerStub) Issue(_ context.Context, inboxID message.InboxID, scopes auth.ScopeSet, expiresAt time.Time) (auth.IssuedCapability, error) {
	if s.err != nil {
		return auth.IssuedCapability{}, s.err
	}
	return auth.IssuedCapability{InboxID: inboxID, Plaintext: "test-capability-placeholder", Scopes: scopes, ExpiresAt: expiresAt}, nil
}

type contentStub struct{ raw string }

func (*contentStub) Delete(message.ContentRef) error { return nil }
func (s *contentStub) OpenRaw(context.Context, message.ContentRef) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(s.raw)), nil
}

var _ Repository = (*repositoryStub)(nil)
var _ CapabilityIssuer = (*issuerStub)(nil)
var _ ContentDeleter = (*contentStub)(nil)
