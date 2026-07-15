package mail

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestOpenAttachmentStreamsDecodedLeaf(t *testing.T) {
	parser, err := NewParser(DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	raw := `From: sender@example.com
Content-Type: multipart/mixed; boundary="boundary"

--boundary
Content-Type: text/plain

hello
--boundary
Content-Type: application/octet-stream
Content-Disposition: attachment; filename="report.txt"
Content-Transfer-Encoding: base64

SGVsbG8gYXR0YWNobWVudCE=
--boundary--
`
	stream, err := parser.OpenAttachment(context.Background(), io.NopCloser(strings.NewReader(raw)), "2")
	if err != nil {
		t.Fatalf("OpenAttachment() error = %v", err)
	}
	defer stream.Close()
	content, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(content) != "Hello attachment!" {
		t.Fatalf("content = %q", content)
	}
}

func TestOpenAttachmentRejectsUnknownPart(t *testing.T) {
	parser, err := NewParser(DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	_, err = parser.OpenAttachment(context.Background(), io.NopCloser(strings.NewReader("Content-Type: text/plain\n\nbody\n")), "9")
	if err == nil {
		t.Fatal("OpenAttachment() error = nil, want not found")
	}
}

func TestOpenAttachmentEnforcesDecodedPartLimit(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxPartBytes = 4
	parser, err := NewParser(limits)
	if err != nil {
		t.Fatal(err)
	}
	raw := "Content-Type: application/octet-stream\nContent-Disposition: attachment\n\n12345"
	stream, err := parser.OpenAttachment(context.Background(), io.NopCloser(strings.NewReader(raw)), "0")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := io.ReadAll(stream); !errors.Is(err, ErrPartTooLarge) {
		t.Fatalf("ReadAll() error = %v, want ErrPartTooLarge", err)
	}
}
