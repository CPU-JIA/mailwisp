package cloudflaretemp

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"mailwisp/internal/auth"
	"mailwisp/internal/mailbox"
	"mailwisp/internal/message"
)

func TestCreateAddressProjectsOpaqueCapabilityAndStableIntegerID(t *testing.T) {
	mailboxes := &mailboxStub{}
	service, err := NewService(mailboxes, &idRepositoryStub{}, []string{"mailwisp.test"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateAddress(context.Background(), "Demo.Name", "MAILWISP.TEST")
	if err != nil {
		t.Fatal(err)
	}
	if mailboxes.created.LocalPart != "demoname" || mailboxes.created.Domain != "MAILWISP.TEST" {
		t.Fatalf("canonical create request = %+v", mailboxes.created)
	}
	if created.AddressID != 42 || created.Capability.Plaintext != "wisp-capability" {
		t.Fatalf("created = %+v", created)
	}
}

func TestCreateAddressCompensatesMappingFailure(t *testing.T) {
	mailboxes := &mailboxStub{}
	repository := &idRepositoryStub{ensureInboxErr: errors.New("database unavailable")}
	service, err := NewService(mailboxes, repository, []string{"mailwisp.test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateAddress(context.Background(), "demo", "mailwisp.test"); err == nil {
		t.Fatal("CreateAddress() error = nil")
	}
	if mailboxes.deleted != testInboxID {
		t.Fatalf("compensated Inbox = %q", mailboxes.deleted)
	}
	if _, err := service.CreateAddress(context.Background(), "***", "mailwisp.test"); !errors.Is(err, ErrInvalidAddressName) {
		t.Fatalf("invalid name error = %v", err)
	}
}

type mailboxStub struct {
	created mailbox.CreateRequest
	deleted message.InboxID
}

func (s *mailboxStub) Create(_ context.Context, request mailbox.CreateRequest) (mailbox.CreatedInbox, error) {
	s.created = request
	return mailbox.CreatedInbox{
		Inbox:      mailbox.Inbox{ID: testInboxID, Address: request.LocalPart + "@mailwisp.test"},
		Capability: auth.IssuedCapability{InboxID: testInboxID, Plaintext: "wisp-capability"},
	}, nil
}
func (*mailboxStub) Get(context.Context, message.InboxID) (mailbox.Inbox, error) {
	return mailbox.Inbox{}, nil
}
func (s *mailboxStub) Delete(_ context.Context, inboxID message.InboxID) error {
	s.deleted = inboxID
	return nil
}
func (*mailboxStub) ListMessagePage(context.Context, message.InboxID, int, int) (mailbox.MessagePage, error) {
	return mailbox.MessagePage{}, nil
}
func (*mailboxStub) GetMessage(context.Context, message.InboxID, message.MessageID) (mailbox.MessageDetail, error) {
	return mailbox.MessageDetail{}, nil
}
func (*mailboxStub) DeleteMessage(context.Context, message.InboxID, message.MessageID) error {
	return nil
}
func (*mailboxStub) OpenSource(context.Context, message.InboxID, message.MessageID) (mailbox.RawSource, error) {
	return mailbox.RawSource{Reader: io.NopCloser(strings.NewReader("raw")), Size: 3}, nil
}

type idRepositoryStub struct{ ensureInboxErr error }

func (s *idRepositoryStub) EnsureInboxID(context.Context, message.InboxID) (int64, error) {
	return 42, s.ensureInboxErr
}
func (*idRepositoryStub) EnsureMessageIDs(_ context.Context, _ message.InboxID, ids []message.MessageID) (map[message.MessageID]int64, error) {
	result := make(map[message.MessageID]int64, len(ids))
	for index, id := range ids {
		result[id] = int64(index + 1)
	}
	return result, nil
}
func (*idRepositoryStub) FindMessageID(context.Context, message.InboxID, int64) (message.MessageID, error) {
	return testMessageID, nil
}

const (
	testInboxID   = message.InboxID("018f26e5-8f04-7b44-8ba2-4a8f434dcb12")
	testMessageID = message.MessageID("018f26e5-8f04-7b44-8ba2-4a8f434dcb13")
)
