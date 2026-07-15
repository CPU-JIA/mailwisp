package message

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestReceiverStoresContentBeforeMetadata(t *testing.T) {
	t.Parallel()

	inboxID := InboxID("018f26e5-8f04-7b44-8ba2-4a8f434dcb12")
	steps := make([]string, 0, 2)
	store := &contentStoreStub{
		put: func(_ context.Context, source io.Reader) (ContentRef, error) {
			steps = append(steps, "content")
			content, err := io.ReadAll(source)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			return ContentRef{Key: testContentKey, SizeBytes: int64(len(content))}, nil
		},
	}
	repository := &repositoryStub{
		commit: func(_ context.Context, delivery Delivery) ([]StoredMessage, error) {
			steps = append(steps, "metadata")
			if delivery.Content.Key != testContentKey {
				t.Fatalf("delivery content key = %q", delivery.Content.Key)
			}
			return []StoredMessage{{ID: "message-id", InboxID: inboxID}}, nil
		},
	}
	receiver, err := NewReceiver(store, repository)
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}
	receiver.now = func() time.Time { return time.Date(2026, 7, 14, 6, 0, 0, 0, time.FixedZone("CST", 8*60*60)) }

	receipt, err := receiver.Receive(context.Background(), ReceiveRequest{
		EnvelopeSender: "sender@example.com",
		Recipients:     []InboxID{inboxID},
		Raw:            bytes.NewReader([]byte("raw message")),
	})
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}
	if len(steps) != 2 || steps[0] != "content" || steps[1] != "metadata" {
		t.Fatalf("Receive() steps = %v, want [content metadata]", steps)
	}
	if receipt.Content.SizeBytes != int64(len("raw message")) || len(receipt.Messages) != 1 {
		t.Fatalf("Receive() receipt = %+v", receipt)
	}
}

func TestReceiverRejectsInvalidRequestBeforeStorage(t *testing.T) {
	t.Parallel()

	called := false
	receiver, err := NewReceiver(&contentStoreStub{put: func(context.Context, io.Reader) (ContentRef, error) {
		called = true
		return ContentRef{}, nil
	}}, &repositoryStub{})
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}

	_, err = receiver.Receive(context.Background(), ReceiveRequest{Raw: bytes.NewReader(nil)})
	if !errors.Is(err, ErrInvalidDelivery) {
		t.Fatalf("Receive() error = %v, want ErrInvalidDelivery", err)
	}
	if called {
		t.Fatal("content store called for invalid request")
	}
}

func TestReceiverPreservesStoreAndRepositoryErrors(t *testing.T) {
	t.Parallel()

	inboxID := InboxID("018f26e5-8f04-7b44-8ba2-4a8f434dcb12")
	storeErr := errors.New("store unavailable")
	repositoryErr := errors.New("database unavailable")

	t.Run("store", func(t *testing.T) {
		receiver, err := NewReceiver(&contentStoreStub{put: func(context.Context, io.Reader) (ContentRef, error) {
			return ContentRef{}, storeErr
		}}, &repositoryStub{})
		if err != nil {
			t.Fatalf("NewReceiver() error = %v", err)
		}
		_, err = receiver.Receive(context.Background(), validRequest(inboxID))
		if !errors.Is(err, storeErr) {
			t.Fatalf("Receive() error = %v, want store error", err)
		}
	})

	t.Run("repository", func(t *testing.T) {
		receiver, err := NewReceiver(&contentStoreStub{put: func(context.Context, io.Reader) (ContentRef, error) {
			return ContentRef{Key: testContentKey, SizeBytes: 1}, nil
		}}, &repositoryStub{commit: func(context.Context, Delivery) ([]StoredMessage, error) {
			return nil, repositoryErr
		}})
		if err != nil {
			t.Fatalf("NewReceiver() error = %v", err)
		}
		_, err = receiver.Receive(context.Background(), validRequest(inboxID))
		if !errors.Is(err, repositoryErr) {
			t.Fatalf("Receive() error = %v, want repository error", err)
		}
	})
}

func TestReceiverDelegatesCapacityCheck(t *testing.T) {
	t.Parallel()

	receiver, err := NewReceiver(&contentStoreStub{check: func(context.Context) error {
		return ErrInsufficientStorage
	}}, &repositoryStub{})
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.CheckCapacity(context.Background()); !errors.Is(err, ErrInsufficientStorage) {
		t.Fatalf("CheckCapacity() error = %v", err)
	}
}

func TestNewReceiverValidatesDependencies(t *testing.T) {
	t.Parallel()

	if _, err := NewReceiver(nil, &repositoryStub{}); err == nil {
		t.Fatal("NewReceiver(nil store) error = nil, want error")
	}
	if _, err := NewReceiver(&contentStoreStub{}, nil); err == nil {
		t.Fatal("NewReceiver(nil repository) error = nil, want error")
	}
}

func validRequest(inboxID InboxID) ReceiveRequest {
	return ReceiveRequest{
		EnvelopeSender: "sender@example.com",
		Recipients:     []InboxID{inboxID},
		Raw:            bytes.NewReader([]byte("x")),
	}
}

type contentStoreStub struct {
	check func(context.Context) error
	put   func(context.Context, io.Reader) (ContentRef, error)
}

func (s *contentStoreStub) CheckCapacity(ctx context.Context) error {
	if s.check == nil {
		return nil
	}
	return s.check(ctx)
}

func (s *contentStoreStub) Put(ctx context.Context, source io.Reader) (ContentRef, error) {
	if s.put == nil {
		return ContentRef{}, errors.New("unexpected content store call")
	}
	return s.put(ctx, source)
}

type repositoryStub struct {
	commit func(context.Context, Delivery) ([]StoredMessage, error)
}

func (s *repositoryStub) CommitDelivery(ctx context.Context, delivery Delivery) ([]StoredMessage, error) {
	if s.commit == nil {
		return nil, errors.New("unexpected repository call")
	}
	return s.commit(ctx, delivery)
}
