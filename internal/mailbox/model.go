// Package mailbox implements anonymous Inbox lifecycle and message access.
package mailbox

import (
	"errors"
	"io"
	"time"

	"mailwisp/internal/mail"
	"mailwisp/internal/message"
)

var (
	// ErrInboxNotFound hides missing, expired, and disabled Inbox state.
	ErrInboxNotFound = errors.New("inbox not found")
	// ErrMessageNotFound hides missing messages and ownership mismatches.
	ErrMessageNotFound = errors.New("message not found")
	// ErrAddressConflict indicates a generated address collision.
	ErrAddressConflict = errors.New("inbox address conflict")
	// ErrInvalidDomain indicates a domain outside the configured public allowlist.
	ErrInvalidDomain = errors.New("invalid inbox domain")
	// ErrInvalidLifetime indicates an unsupported Inbox lifetime.
	ErrInvalidLifetime = errors.New("invalid inbox lifetime")
	// ErrInvalidLocalPart indicates an unsupported requested address prefix.
	ErrInvalidLocalPart = errors.New("invalid inbox local part")
)

// Inbox is one temporary receiving address.
type Inbox struct {
	ID        message.InboxID `json:"id"`
	Address   string          `json:"address"`
	Status    string          `json:"status"`
	ExpiresAt time.Time       `json:"expires_at"`
	CreatedAt time.Time       `json:"created_at"`
}

// NewInbox contains validated persistence material.
type NewInbox struct {
	Address   string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// MessageSummary is the bounded representation used in Inbox lists.
type MessageSummary struct {
	ID             message.MessageID `json:"id"`
	EnvelopeSender string            `json:"envelope_sender"`
	Subject        string            `json:"subject"`
	Preview        string            `json:"preview"`
	ReceivedAt     time.Time         `json:"received_at"`
	ParseStatus    string            `json:"parse_status"`
	SizeBytes      int64             `json:"size_bytes"`
	HasAttachments bool              `json:"has_attachments"`
	Seen           bool              `json:"seen"`
}

// MessageDetail contains parsed, untrusted email content.
type MessageDetail struct {
	MessageSummary
	HeaderMessageID string            `json:"header_message_id"`
	From            []mail.Address    `json:"from"`
	To              []mail.Address    `json:"to"`
	Cc              []mail.Address    `json:"cc"`
	SentAt          *time.Time        `json:"sent_at"`
	Text            string            `json:"text"`
	HTMLSource      mail.UnsafeHTML   `json:"html_source"`
	Attachments     []mail.Attachment `json:"attachments"`
	Warnings        []mail.Warning    `json:"warnings"`
}

// Page bounds one deterministic message listing.
type Page struct {
	Limit  int
	Offset int
}

// MessagePage contains one bounded page and complete Inbox counters.
type MessagePage struct {
	Items  []MessageSummary
	Total  int
	Unread int
}

// RawSource is one owned immutable RFC 822 message stream.
type RawSource struct {
	Reader io.ReadCloser
	Size   int64
}

// AttachmentSource is one owned decoded attachment stream.
type AttachmentSource struct {
	Reader      io.ReadCloser
	FileName    string
	ContentType string
	Size        int64
}
