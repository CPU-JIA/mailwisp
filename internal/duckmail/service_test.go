package duckmail

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

func TestServiceCreatesPasswordAccountAndLogsIn(t *testing.T) {
	repository := &duckRepositoryStub{}
	issuer := &duckIssuerStub{}
	service, err := NewService(repository, &duckMailboxStub{}, issuer, Options{PublicDomains: []string{"mailwisp.test"}, DefaultTTL: time.Hour, MaxTTL: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC) }
	inbox, err := service.CreateAccount(context.Background(), CreateAccountRequest{Address: "Demo@MailWisp.Test", Password: "secret-password"})
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	if inbox.Address != "demo@mailwisp.test" || repository.account.PasswordHash == "secret-password" {
		t.Fatalf("created Inbox/hash = %+v/%q", inbox, repository.account.PasswordHash)
	}
	issued, err := service.Login(context.Background(), inbox.Address, "secret-password")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if issued.Plaintext != "duckmail-test-token" || !issued.Scopes.Has(auth.ScopeMessageUpdate) {
		t.Fatalf("issued = %+v", issued)
	}
	if _, err := service.Login(context.Background(), inbox.Address, "wrong"); !errors.Is(err, ErrLoginFailed) {
		t.Fatalf("Login(wrong) error = %v", err)
	}
}

func TestServiceRejectsPermanentAndInvalidAccounts(t *testing.T) {
	service, err := NewService(&duckRepositoryStub{}, &duckMailboxStub{}, &duckIssuerStub{}, Options{PublicDomains: []string{"mailwisp.test"}, DefaultTTL: time.Hour, MaxTTL: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	permanent := int64(0)
	if _, err := service.CreateAccount(context.Background(), CreateAccountRequest{Address: "demo@mailwisp.test", Password: "secret", ExpiresIn: &permanent}); !errors.Is(err, ErrPermanentUnsupported) {
		t.Fatalf("permanent account error = %v", err)
	}
	if _, err := service.CreateAccount(context.Background(), CreateAccountRequest{Address: "x@evil.test", Password: "secret"}); !errors.Is(err, ErrInvalidAccount) {
		t.Fatalf("invalid account error = %v", err)
	}
	if _, err := service.CreateAccount(context.Background(), CreateAccountRequest{Address: "a..b@mailwisp.test", Password: "secret"}); !errors.Is(err, ErrInvalidAccount) {
		t.Fatalf("double-dot local part error = %v", err)
	}
}

type duckRepositoryStub struct{ account NewAccount }

func (r *duckRepositoryStub) CreateAccount(_ context.Context, account NewAccount) (mailbox.Inbox, error) {
	r.account = account
	return mailbox.Inbox{ID: message.InboxID("018f26e5-8f04-7b44-8ba2-4a8f434dcb12"), Address: account.Address, Status: "active", CreatedAt: account.CreatedAt, ExpiresAt: account.ExpiresAt}, nil
}
func (r *duckRepositoryStub) FindAccountByAddress(_ context.Context, address string) (Account, error) {
	if r.account.Address != address {
		return Account{}, ErrLoginFailed
	}
	return Account{Inbox: mailbox.Inbox{ID: message.InboxID("018f26e5-8f04-7b44-8ba2-4a8f434dcb12"), Address: address, Status: "active", CreatedAt: r.account.CreatedAt, ExpiresAt: r.account.ExpiresAt}, PasswordHash: r.account.PasswordHash}, nil
}

type duckIssuerStub struct{}

func (*duckIssuerStub) Issue(_ context.Context, inboxID message.InboxID, scopes auth.ScopeSet, expiresAt time.Time) (auth.IssuedCapability, error) {
	return auth.IssuedCapability{InboxID: inboxID, Plaintext: "duckmail-test-token", Scopes: scopes, ExpiresAt: expiresAt}, nil
}

type duckMailboxStub struct{}

func (*duckMailboxStub) Get(context.Context, message.InboxID) (mailbox.Inbox, error) {
	return mailbox.Inbox{}, nil
}
func (*duckMailboxStub) Delete(context.Context, message.InboxID) error { return nil }
func (*duckMailboxStub) ListMessagePage(context.Context, message.InboxID, int, int) (mailbox.MessagePage, error) {
	return mailbox.MessagePage{}, nil
}
func (*duckMailboxStub) GetMessage(context.Context, message.InboxID, message.MessageID) (mailbox.MessageDetail, error) {
	return mailbox.MessageDetail{}, nil
}
func (*duckMailboxStub) DeleteMessage(context.Context, message.InboxID, message.MessageID) error {
	return nil
}
func (*duckMailboxStub) MarkMessageSeen(context.Context, message.InboxID, message.MessageID) error {
	return nil
}
func (*duckMailboxStub) OpenSource(context.Context, message.InboxID, message.MessageID) (mailbox.RawSource, error) {
	return mailbox.RawSource{Reader: io.NopCloser(strings.NewReader(""))}, nil
}
