package message

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// ContentStore durably stores immutable raw message bytes.
type ContentStore interface {
	Put(context.Context, io.Reader) (ContentRef, error)
}

// DeliveryRepository atomically commits message metadata for all recipients.
type DeliveryRepository interface {
	CommitDelivery(context.Context, Delivery) ([]StoredMessage, error)
}

// ReceiveRequest contains an already-authorized LMTP delivery.
type ReceiveRequest struct {
	EnvelopeSender string
	Recipients     []InboxID
	Raw            io.Reader
}

// Receipt records the durable result of one receive operation.
type Receipt struct {
	Content  ContentRef
	Messages []StoredMessage
}

// Receiver coordinates raw-content durability with the metadata transaction.
type Receiver struct {
	contentStore ContentStore
	repository   DeliveryRepository
	now          func() time.Time
}

// NewReceiver constructs a durable message receiver.
func NewReceiver(contentStore ContentStore, repository DeliveryRepository) (*Receiver, error) {
	if contentStore == nil {
		return nil, errors.New("message content store is required")
	}
	if repository == nil {
		return nil, errors.New("message delivery repository is required")
	}
	return &Receiver{
		contentStore: contentStore,
		repository:   repository,
		now:          time.Now,
	}, nil
}

// Receive stores raw bytes first, then atomically commits metadata for every recipient.
//
// If metadata commit fails, the immutable object is deliberately left for the
// orphan reconciler. It must not be deleted blindly because identical content
// may already be referenced by another delivery.
func (r *Receiver) Receive(ctx context.Context, request ReceiveRequest) (Receipt, error) {
	if request.Raw == nil {
		return Receipt{}, fmt.Errorf("%w: raw content is required", ErrInvalidDelivery)
	}
	if err := validateEnvelope(request.EnvelopeSender, request.Recipients); err != nil {
		return Receipt{}, err
	}
	receivedAt := r.now().UTC()

	content, err := r.contentStore.Put(ctx, request.Raw)
	if err != nil {
		return Receipt{}, fmt.Errorf("store raw message: %w", err)
	}
	delivery := Delivery{
		Content:        content,
		EnvelopeSender: request.EnvelopeSender,
		Recipients:     request.Recipients,
		ReceivedAt:     receivedAt,
	}
	if err := delivery.Validate(); err != nil {
		return Receipt{}, err
	}

	messages, err := r.repository.CommitDelivery(ctx, delivery)
	if err != nil {
		return Receipt{}, fmt.Errorf("commit delivery metadata: %w", err)
	}

	return Receipt{Content: content, Messages: messages}, nil
}
