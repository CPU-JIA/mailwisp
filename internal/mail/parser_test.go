package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParserDecodesHeadersCharsetAndDate(t *testing.T) {
	parser := newTestParser(t, nil)
	raw := strings.Join([]string{
		"From: =?UTF-8?Q?Sender_Name?= <sender@example.com>",
		"To: first@example.com, Second <second@example.com>",
		"Subject: =?UTF-8?Q?Your_code_123456?=",
		"Message-ID: <message@example.com>",
		"Date: Tue, 14 Jul 2026 18:30:00 +0800",
		"Content-Type: text/plain; charset=iso-8859-1",
		"Content-Transfer-Encoding: quoted-printable",
		"",
		"Ol=E1",
	}, "\r\n")

	parsed, err := parser.Parse(context.Background(), strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if parsed.Subject != "Your code 123456" || parsed.MessageID != "<message@example.com>" {
		t.Fatalf("parsed subject/message ID = %q/%q", parsed.Subject, parsed.MessageID)
	}
	if len(parsed.From) != 1 || parsed.From[0].Name != "Sender Name" || parsed.From[0].Address != "sender@example.com" {
		t.Fatalf("parsed From = %+v", parsed.From)
	}
	if len(parsed.To) != 2 || parsed.To[1].Name != "Second" {
		t.Fatalf("parsed To = %+v", parsed.To)
	}
	if parsed.Text != "Olá" || parsed.HTMLSource != "" {
		t.Fatalf("parsed bodies = %q/%q", parsed.Text, parsed.HTMLSource)
	}
	wantDate := time.Date(2026, 7, 14, 10, 30, 0, 0, time.UTC)
	if parsed.Date == nil || !parsed.Date.Equal(wantDate) {
		t.Fatalf("parsed Date = %v, want %s", parsed.Date, wantDate)
	}
	if len(parsed.Warnings) != 0 {
		t.Fatalf("parsed warnings = %+v", parsed.Warnings)
	}
}

func TestParserWalksNestedBodiesAndAttachmentMetadata(t *testing.T) {
	parser := newTestParser(t, nil)
	attachment := base64.StdEncoding.EncodeToString([]byte("hello"))
	raw := strings.Join([]string{
		"Subject: nested",
		"Content-Type: multipart/mixed; boundary=outer",
		"",
		"--outer",
		"Content-Type: multipart/alternative; boundary=alternative",
		"",
		"--alternative",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"plain body",
		"--alternative",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>html body</p>",
		"--alternative--",
		"--outer",
		"Content-Type: application/pdf",
		"Content-Disposition: attachment; filename=\"../invoice.pdf\"",
		"Content-Transfer-Encoding: base64",
		"",
		attachment,
		"--outer--",
		"",
	}, "\r\n")

	parsed, err := parser.Parse(context.Background(), strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if parsed.Text != "plain body" || parsed.HTMLSource != UnsafeHTML("<p>html body</p>") {
		t.Fatalf("parsed bodies = %q/%q", parsed.Text, parsed.HTMLSource)
	}
	if len(parsed.Attachments) != 1 {
		t.Fatalf("attachment count = %d, want 1", len(parsed.Attachments))
	}
	got := parsed.Attachments[0]
	if got.PartPath != "2" || got.FileName != "_invoice.pdf" || got.ContentType != "application/pdf" || got.SizeBytes != 5 {
		t.Fatalf("attachment = %+v", got)
	}
	if !hasWarning(parsed.Warnings, WarningFilenameNormalized) {
		t.Fatalf("warnings = %+v, want filename normalization", parsed.Warnings)
	}
}

func TestParserRejectsResourceLimitViolations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Limits)
		raw    string
		want   error
	}{
		{
			name: "raw bytes",
			mutate: func(limits *Limits) {
				limits.MaxRawBytes = 64
			},
			raw:  "Subject: oversized\r\n\r\n" + strings.Repeat("x", 80),
			want: ErrMessageTooLarge,
		},
		{
			name: "root header bytes",
			mutate: func(limits *Limits) {
				limits.MaxHeaderBytes = 32
			},
			raw:  "Subject: " + strings.Repeat("x", 40) + "\r\n\r\nbody",
			want: ErrHeaderTooLarge,
		},
		{
			name: "nested header bytes",
			mutate: func(limits *Limits) {
				limits.MaxHeaderBytes = 64
			},
			raw: strings.Join([]string{
				"Content-Type: multipart/mixed; boundary=parts",
				"",
				"--parts",
				"Content-Type: text/plain",
				"X-Oversized: " + strings.Repeat("x", 64),
				"",
				"body",
				"--parts--",
			}, "\r\n"),
			want: ErrHeaderTooLarge,
		},
		{
			name: "header count",
			mutate: func(limits *Limits) {
				limits.MaxHeaders = 2
			},
			raw:  "Subject: x\r\nFrom: a@example.com\r\nTo: b@example.com\r\n\r\nbody",
			want: ErrTooManyHeaders,
		},
		{
			name: "part count",
			mutate: func(limits *Limits) {
				limits.MaxParts = 2
			},
			raw:  multipartWithTextParts(2),
			want: ErrTooManyParts,
		},
		{
			name: "nesting depth",
			mutate: func(limits *Limits) {
				limits.MaxDepth = 1
			},
			raw:  nestedMultipart(2),
			want: ErrNestingTooDeep,
		},
		{
			name: "part bytes",
			mutate: func(limits *Limits) {
				limits.MaxPartBytes = 4
			},
			raw:  "Content-Type: text/plain\r\n\r\n12345",
			want: ErrPartTooLarge,
		},
		{
			name: "total decoded bytes",
			mutate: func(limits *Limits) {
				limits.MaxPartBytes = 5
				limits.MaxDecodedBytes = 6
			},
			raw:  multipartWithTextParts(2),
			want: ErrDecodedContentTooLarge,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parser := newTestParser(t, test.mutate)
			_, err := parser.Parse(context.Background(), strings.NewReader(test.raw))
			if !errors.Is(err, test.want) {
				t.Fatalf("Parse() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestParserTruncatesPreviewsWithoutSkippingDecodedAccounting(t *testing.T) {
	parser := newTestParser(t, func(limits *Limits) {
		limits.MaxTextBytes = 4
		limits.MaxHTMLBytes = 5
	})
	raw := strings.Join([]string{
		"Content-Type: multipart/alternative; boundary=parts",
		"",
		"--parts",
		"Content-Type: text/plain",
		"",
		"123456",
		"--parts",
		"Content-Type: text/html",
		"",
		"<b>123</b>",
		"--parts--",
	}, "\r\n")

	parsed, err := parser.Parse(context.Background(), strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if parsed.Text != "1234" || parsed.HTMLSource != "<b>12" {
		t.Fatalf("truncated bodies = %q/%q", parsed.Text, parsed.HTMLSource)
	}
	if !hasWarning(parsed.Warnings, WarningTextTruncated) || !hasWarning(parsed.Warnings, WarningHTMLTruncated) {
		t.Fatalf("warnings = %+v", parsed.Warnings)
	}
}

func TestParserRecoversUnknownCharsetAsWarning(t *testing.T) {
	parser := newTestParser(t, nil)
	raw := "Content-Type: text/plain; charset=x-mailwisp-unknown\r\n\r\nbody"
	parsed, err := parser.Parse(context.Background(), strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if parsed.Text != "body" || !hasWarning(parsed.Warnings, WarningUnknownCharset) {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestParserHonorsCancellation(t *testing.T) {
	parser := newTestParser(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := parser.Parse(ctx, strings.NewReader("Subject: canceled\r\n\r\nbody"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Parse() error = %v, want context.Canceled", err)
	}
}

func TestParserIsSafeForConcurrentUse(t *testing.T) {
	parser := newTestParser(t, nil)
	raw := []byte("Subject: concurrent\r\nContent-Type: text/plain\r\n\r\nbody")
	const workers = 32
	errorsChannel := make(chan error, workers)
	for range workers {
		go func() {
			parsed, err := parser.Parse(context.Background(), bytes.NewReader(raw))
			if err == nil && (parsed.Subject != "concurrent" || parsed.Text != "body") {
				err = fmt.Errorf("unexpected parsed result: %+v", parsed)
			}
			errorsChannel <- err
		}()
	}
	for range workers {
		if err := <-errorsChannel; err != nil {
			t.Errorf("concurrent Parse() error = %v", err)
		}
	}
}

func TestNewParserRejectsUnboundedLimits(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxParts = 0
	if _, err := NewParser(limits); err == nil {
		t.Fatal("NewParser() error = nil, want invalid limit")
	}
	limits = DefaultLimits()
	limits.MaxHeaderBytes = limits.MaxTotalHeaderBytes + 1
	if _, err := NewParser(limits); err == nil {
		t.Fatal("NewParser() error = nil, want inconsistent header limits")
	}
}

func newTestParser(t *testing.T, mutate func(*Limits)) *Parser {
	t.Helper()
	limits := DefaultLimits()
	limits.MaxRawBytes = 1 << 20
	limits.MaxDecodedBytes = 1 << 20
	limits.MaxPartBytes = 1 << 20
	if mutate != nil {
		mutate(&limits)
	}
	parser, err := NewParser(limits)
	if err != nil {
		t.Fatalf("NewParser() error = %v", err)
	}
	return parser
}

func hasWarning(warnings []Warning, code WarningCode) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

func multipartWithTextParts(parts int) string {
	var raw strings.Builder
	raw.WriteString("Content-Type: multipart/mixed; boundary=parts\r\n\r\n")
	for index := range parts {
		fmt.Fprintf(&raw, "--parts\r\nContent-Type: text/plain\r\n\r\npart%d\r\n", index)
	}
	raw.WriteString("--parts--\r\n")
	return raw.String()
}

func nestedMultipart(depth int) string {
	var raw strings.Builder
	for index := range depth {
		fmt.Fprintf(&raw, "Content-Type: multipart/mixed; boundary=level%d\r\n\r\n--level%d\r\n", index, index)
	}
	raw.WriteString("Content-Type: text/plain\r\n\r\nbody\r\n")
	for index := depth - 1; index >= 0; index-- {
		fmt.Fprintf(&raw, "--level%d--\r\n", index)
	}
	return raw.String()
}

func FuzzParserNeverPanics(f *testing.F) {
	for _, seed := range []string{
		"Subject: seed\r\n\r\nbody",
		multipartWithTextParts(2),
		nestedMultipart(3),
		"Content-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\n!!!!",
	} {
		f.Add([]byte(seed))
	}
	limits := DefaultLimits()
	limits.MaxRawBytes = 64 << 10
	limits.MaxHeaderBytes = 8 << 10
	limits.MaxTotalHeaderBytes = 16 << 10
	limits.MaxDecodedBytes = 64 << 10
	limits.MaxPartBytes = 32 << 10
	limits.MaxTextBytes = 8 << 10
	limits.MaxHTMLBytes = 8 << 10
	parser, err := NewParser(limits)
	if err != nil {
		f.Fatalf("NewParser() error = %v", err)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		parsed, _ := parser.Parse(context.Background(), bytes.NewReader(raw))
		if len(parsed.Text) > limits.MaxTextBytes || len(parsed.HTMLSource) > limits.MaxHTMLBytes {
			t.Fatalf("parser exceeded preview limits: text=%d html=%d", len(parsed.Text), len(parsed.HTMLSource))
		}
		if len(parsed.Attachments) > limits.MaxParts || len(parsed.Warnings) > limits.MaxWarnings {
			t.Fatalf("parser exceeded collection limits: attachments=%d warnings=%d", len(parsed.Attachments), len(parsed.Warnings))
		}
	})
}

func BenchmarkParserMultipart(b *testing.B) {
	attachment := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 32<<10)))
	raw := strings.Join([]string{
		"From: sender@example.com",
		"To: inbox@example.test",
		"Subject: benchmark",
		"Content-Type: multipart/mixed; boundary=benchmark",
		"",
		"--benchmark",
		"Content-Type: text/plain; charset=utf-8",
		"",
		strings.Repeat("preview ", 512),
		"--benchmark",
		"Content-Type: application/octet-stream",
		"Content-Disposition: attachment; filename=sample.bin",
		"Content-Transfer-Encoding: base64",
		"",
		attachment,
		"--benchmark--",
	}, "\r\n")
	parser, err := NewParser(DefaultLimits())
	if err != nil {
		b.Fatalf("NewParser() error = %v", err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	b.ResetTimer()
	for range b.N {
		if _, err := parser.Parse(context.Background(), strings.NewReader(raw)); err != nil {
			b.Fatalf("Parse() error = %v", err)
		}
	}
}
