// Package message defines durable incoming-message application semantics.
package message

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrInvalidDelivery indicates that a delivery violates an application invariant.
	ErrInvalidDelivery = errors.New("invalid mail delivery")
	// ErrInboxNotFound indicates that a recipient inbox does not exist or cannot receive mail.
	ErrInboxNotFound = errors.New("inbox not found")
	// ErrContentTooLarge indicates that raw message content exceeds the configured limit.
	ErrContentTooLarge = errors.New("message content too large")
	// ErrInboxMessageQuotaExceeded indicates that one recipient reached its message-count limit.
	ErrInboxMessageQuotaExceeded = errors.New("inbox message quota exceeded")
	// ErrInboxStorageQuotaExceeded indicates that one recipient reached its logical byte limit.
	ErrInboxStorageQuotaExceeded = errors.New("inbox storage quota exceeded")
	// ErrInsufficientStorage indicates that durable content capacity is temporarily unavailable.
	ErrInsufficientStorage = errors.New("insufficient durable storage")
)

const sha256HexLength = 64

// InboxID identifies one canonical MailWisp inbox.
type InboxID string

// MessageID identifies one message record.
type MessageID string

// ContentRef identifies immutable raw content and its expected byte length.
type ContentRef struct {
	Key       string
	SizeBytes int64
}

// Delivery describes one durably stored SMTP payload delivered to one or more inboxes.
type Delivery struct {
	Content        ContentRef
	EnvelopeSender string
	Recipients     []InboxID
	ReceivedAt     time.Time
}

// StoredMessage identifies a message row created for one recipient inbox.
type StoredMessage struct {
	ID      MessageID
	InboxID InboxID
}

// Validate checks all invariants required before a delivery transaction begins.
func (d Delivery) Validate() error {
	if err := d.Content.Validate(); err != nil {
		return err
	}
	if d.ReceivedAt.IsZero() {
		return fmt.Errorf("%w: received time is required", ErrInvalidDelivery)
	}
	return validateEnvelope(d.EnvelopeSender, d.Recipients)
}

// Validate checks that a content reference uses the canonical SHA-256 key format.
func (r ContentRef) Validate() error {
	const prefix = "sha256/"
	if !strings.HasPrefix(r.Key, prefix) {
		return fmt.Errorf("%w: content key must use sha256", ErrInvalidDelivery)
	}
	digest := strings.TrimPrefix(r.Key, prefix)
	if len(digest) != sha256HexLength || strings.ToLower(digest) != digest {
		return fmt.Errorf("%w: content digest must be 64 lowercase hexadecimal characters", ErrInvalidDelivery)
	}
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256HexLength/2 {
		return fmt.Errorf("%w: content digest is invalid", ErrInvalidDelivery)
	}
	if r.SizeBytes < 0 {
		return fmt.Errorf("%w: content size must not be negative", ErrInvalidDelivery)
	}
	return nil
}

func validateEnvelope(envelopeSender string, recipients []InboxID) error {
	if len(envelopeSender) > 320 {
		return fmt.Errorf("%w: envelope sender exceeds 320 bytes", ErrInvalidDelivery)
	}
	if len(recipients) == 0 {
		return fmt.Errorf("%w: at least one recipient is required", ErrInvalidDelivery)
	}

	seen := make(map[InboxID]struct{}, len(recipients))
	for index, recipient := range recipients {
		if _, err := uuid.Parse(string(recipient)); err != nil {
			return fmt.Errorf("%w: recipient %d is not a valid UUID", ErrInvalidDelivery, index)
		}
		if _, exists := seen[recipient]; exists {
			return fmt.Errorf("%w: duplicate recipient %q", ErrInvalidDelivery, recipient)
		}
		seen[recipient] = struct{}{}
	}
	return nil
}
