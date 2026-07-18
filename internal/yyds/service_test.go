package yyds

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"mailwisp/internal/auth"
	"mailwisp/internal/mailbox"
	"mailwisp/internal/message"
)

func TestCreateAccountMapsAddressToCanonicalInbox(t *testing.T) {
	mailboxes := &mailboxStub{}
	service, err := NewService(mailboxes, &credentialStub{}, []string{"mailwisp.test"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateAccount(context.Background(), CreateAccountRequest{Address: "Demo@MailWisp.Test"})
	if err != nil {
		t.Fatal(err)
	}
	if mailboxes.created.LocalPart != "demo" || mailboxes.created.Domain != "mailwisp.test" || created.Capability.Plaintext == "" {
		t.Fatalf("created request/result = %+v/%+v", mailboxes.created, created)
	}
	if _, err := service.CreateAccount(context.Background(), CreateAccountRequest{Address: "a@one.test", Domain: "two.test"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("domain mismatch error = %v", err)
	}
	if _, err := service.CreateAccount(context.Background(), CreateAccountRequest{Address: "Legacy", Domain: "mailwisp.test"}); err != nil || mailboxes.created.LocalPart != "legacy" || mailboxes.created.Domain != "mailwisp.test" {
		t.Fatalf("legacy prefix request = %+v, error = %v", mailboxes.created, err)
	}
	for _, address := range []string{"@mailwisp.test", "demo@", "demo@mailwisp.test@invalid", "a..b@mailwisp.test"} {
		if _, err := service.CreateAccount(context.Background(), CreateAccountRequest{Address: address}); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("invalid address %q error = %v", address, err)
		}
	}
}

func TestRefreshTokenRequiresMatchingInboxAddress(t *testing.T) {
	mailboxes := &mailboxStub{}
	credentials := &credentialStub{}
	service, err := NewService(mailboxes, credentials, []string{"mailwisp.test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.RefreshToken(context.Background(), "old", "other@mailwisp.test"); !errors.Is(err, ErrAddressMismatch) {
		t.Fatalf("mismatch error = %v", err)
	}
	rotated, inbox, err := service.RefreshToken(context.Background(), "old", "demo@mailwisp.test")
	if err != nil || rotated.Plaintext != "rotated" || inbox.Address != "demo@mailwisp.test" {
		t.Fatalf("refresh = %+v/%+v, error = %v", rotated, inbox, err)
	}
}

type mailboxStub struct{ created mailbox.CreateRequest }

func (s *mailboxStub) Create(_ context.Context, request mailbox.CreateRequest) (mailbox.CreatedInbox, error) {
	s.created = request
	return mailbox.CreatedInbox{Inbox: mailbox.Inbox{ID: testInboxID, Address: request.LocalPart + "@" + request.Domain}, Capability: auth.IssuedCapability{Plaintext: "created"}}, nil
}
func (*mailboxStub) Get(context.Context, message.InboxID) (mailbox.Inbox, error) {
	return mailbox.Inbox{ID: testInboxID, Address: "demo@mailwisp.test", Status: "active", ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (*mailboxStub) Delete(context.Context, message.InboxID) error { return nil }
func (*mailboxStub) ListMessagePage(context.Context, message.InboxID, int, int) (mailbox.MessagePage, error) {
	return mailbox.MessagePage{}, nil
}
func (*mailboxStub) GetMessage(context.Context, message.InboxID, message.MessageID) (mailbox.MessageDetail, error) {
	return mailbox.MessageDetail{}, nil
}
func (*mailboxStub) DeleteMessage(context.Context, message.InboxID, message.MessageID) error {
	return nil
}
func (*mailboxStub) MarkMessageSeen(context.Context, message.InboxID, message.MessageID) error {
	return nil
}
func (*mailboxStub) OpenSource(context.Context, message.InboxID, message.MessageID) (mailbox.RawSource, error) {
	return mailbox.RawSource{Reader: io.NopCloser(strings.NewReader("raw")), Size: 3}, nil
}
func (*mailboxStub) OpenAttachment(context.Context, message.InboxID, message.MessageID, string) (mailbox.AttachmentSource, error) {
	return mailbox.AttachmentSource{}, mailbox.ErrMessageNotFound
}

type credentialStub struct{}

func (*credentialStub) Authenticate(context.Context, string, ...auth.Scope) (auth.Principal, error) {
	return auth.Principal{InboxID: testInboxID}, nil
}
func (*credentialStub) Rotate(context.Context, string) (auth.IssuedCapability, error) {
	return auth.IssuedCapability{InboxID: testInboxID, Plaintext: "rotated"}, nil
}

const testInboxID = message.InboxID("018f26e5-8f04-7b44-8ba2-4a8f434dcb12")
