// Package mail parses untrusted MIME messages behind explicit resource limits.
package mail

import "time"

// UnsafeHTML contains decoded but unsanitized email HTML. It must never be
// inserted into a trusted DOM without a separate sanitization and sandbox boundary.
type UnsafeHTML string

// Address contains one untrusted display name and address parsed from a message header.
type Address struct {
	Name    string
	Address string
}

// Attachment contains metadata for one decoded non-body MIME leaf. The bytes
// remain recoverable from the durable Raw MIME source by PartPath.
type Attachment struct {
	PartPath    string
	FileName    string
	ContentType string
	Disposition string
	ContentID   string
	SizeBytes   int64
}

// WarningCode identifies one recoverable parser condition.
type WarningCode string

const (
	WarningUnknownCharset          WarningCode = "unknown_charset"
	WarningUnknownTransferEncoding WarningCode = "unknown_transfer_encoding"
	WarningMalformedContentType    WarningCode = "malformed_content_type"
	WarningMalformedDisposition    WarningCode = "malformed_content_disposition"
	WarningInvalidSubject          WarningCode = "invalid_subject"
	WarningInvalidAddress          WarningCode = "invalid_address"
	WarningInvalidDate             WarningCode = "invalid_date"
	WarningTextTruncated           WarningCode = "text_truncated"
	WarningHTMLTruncated           WarningCode = "html_truncated"
	WarningFilenameNormalized      WarningCode = "filename_normalized"
	WarningListTruncated           WarningCode = "warning_list_truncated"
)

// Warning records a recoverable condition without including raw message content.
type Warning struct {
	Code     WarningCode
	PartPath string
	Detail   string
}

// ParsedMessage is the bounded, decoded representation derived from Raw MIME.
type ParsedMessage struct {
	Subject     string
	MessageID   string
	From        []Address
	To          []Address
	Cc          []Address
	Date        *time.Time
	Text        string
	HTMLSource  UnsafeHTML
	Attachments []Attachment
	Warnings    []Warning
}
