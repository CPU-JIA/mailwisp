package mail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strconv"
	"strings"

	message "github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset"
)

// Parser derives bounded message previews and attachment metadata from Raw MIME.
type Parser struct {
	limits Limits
}

// NewParser constructs a parser with explicit, validated limits.
func NewParser(limits Limits) (*Parser, error) {
	if err := limits.validate(); err != nil {
		return nil, err
	}
	return &Parser{limits: limits}, nil
}

// Parse consumes one Raw MIME message. HTMLSource is decoded but deliberately
// unsanitized; callers must apply a separate HTML policy before presentation.
func (p *Parser) Parse(ctx context.Context, source io.Reader) (ParsedMessage, error) {
	if ctx == nil {
		return ParsedMessage{}, errors.New("mail parser context is required")
	}
	if source == nil {
		return ParsedMessage{}, errors.New("mail parser source is required")
	}
	if err := ctx.Err(); err != nil {
		return ParsedMessage{}, err
	}

	limited := &io.LimitedReader{R: &contextReader{ctx: ctx, source: source}, N: p.limits.MaxRawBytes + 1}
	counted := &countingReader{source: limited}
	entity, readErr := message.ReadWithOptions(counted, &message.ReadOptions{MaxHeaderBytes: p.limits.MaxHeaderBytes})
	state := parseState{limits: p.limits}
	if entity == nil {
		if counted.read > p.limits.MaxRawBytes {
			return ParsedMessage{}, fmt.Errorf("%w: maximum %d bytes", ErrMessageTooLarge, p.limits.MaxRawBytes)
		}
		if isHeaderLimitError(readErr) {
			return ParsedMessage{}, fmt.Errorf("%w: root exceeds %d bytes", ErrHeaderTooLarge, p.limits.MaxHeaderBytes)
		}
		if readErr != nil {
			return ParsedMessage{}, fmt.Errorf("read MIME root: %w", readErr)
		}
		return ParsedMessage{}, errors.New("read MIME root: empty entity")
	}
	if err := state.recoverDecodeError(readErr, "0"); err != nil {
		return ParsedMessage{}, fmt.Errorf("decode MIME root: %w", err)
	}
	if err := state.readRootMetadata(entity.Header); err != nil {
		return ParsedMessage{}, err
	}

	walkErr := entity.Walk(func(path []int, part *message.Entity, decodeErr error) error {
		partPath := formatPartPath(path)
		if len(path) > state.limits.MaxDepth {
			return fmt.Errorf("%w: part %s exceeds depth %d", ErrNestingTooDeep, partPath, state.limits.MaxDepth)
		}
		state.parts++
		if state.parts > state.limits.MaxParts {
			return fmt.Errorf("%w: maximum %d parts", ErrTooManyParts, state.limits.MaxParts)
		}
		if err := state.inspectHeader(part.Header, partPath); err != nil {
			return err
		}
		if err := state.recoverDecodeError(decodeErr, partPath); err != nil {
			return fmt.Errorf("decode MIME part %s: %w", partPath, err)
		}
		return state.consumeEntity(ctx, part, partPath)
	})
	if counted.read > p.limits.MaxRawBytes {
		return ParsedMessage{}, fmt.Errorf("%w: maximum %d bytes", ErrMessageTooLarge, p.limits.MaxRawBytes)
	}
	if err := ctx.Err(); err != nil {
		return ParsedMessage{}, err
	}
	if walkErr != nil {
		return ParsedMessage{}, fmt.Errorf("walk MIME message: %w", walkErr)
	}
	return state.result, nil
}

type parseState struct {
	limits            Limits
	result            ParsedMessage
	parts             int
	headerBytes       int64
	headers           int
	decodedBytes      int64
	warningsTruncated bool
}

func (s *parseState) readRootMetadata(header message.Header) error {
	subject, err := header.Text("Subject")
	if err != nil {
		s.warn(WarningInvalidSubject, "0", "Subject charset could not be decoded")
	}
	s.result.Subject = truncateUTF8(cleanHeaderValue(subject), s.limits.MaxSubjectBytes)
	s.result.MessageID = truncateUTF8(cleanHeaderValue(header.Get("Message-ID")), s.limits.MaxSubjectBytes)
	s.result.From = s.readAddressHeader(header, "From")
	s.result.To = s.readAddressHeader(header, "To")
	s.result.Cc = s.readAddressHeader(header, "Cc")
	if rawDate := strings.TrimSpace(header.Get("Date")); rawDate != "" {
		parsed, err := mail.ParseDate(rawDate)
		if err != nil {
			s.warn(WarningInvalidDate, "0", "Date header could not be parsed")
		} else {
			utc := parsed.UTC()
			s.result.Date = &utc
		}
	}
	return nil
}

func (s *parseState) readAddressHeader(header message.Header, key string) []Address {
	fields := header.FieldsByKey(key)
	addresses := make([]Address, 0, min(fields.Len(), s.limits.MaxAddresses))
	for fields.Next() {
		decoded, err := fields.Text()
		if err != nil {
			s.warn(WarningInvalidAddress, "0", key+" charset could not be decoded")
		}
		parsed, err := mail.ParseAddressList(decoded)
		if err != nil {
			s.warn(WarningInvalidAddress, "0", key+" address list could not be parsed")
			continue
		}
		for _, address := range parsed {
			if len(addresses) >= s.limits.MaxAddresses {
				s.warn(WarningInvalidAddress, "0", key+" address list exceeded its limit")
				return addresses
			}
			addresses = append(addresses, Address{
				Name:    truncateUTF8(cleanHeaderValue(address.Name), 320),
				Address: truncateUTF8(cleanHeaderValue(address.Address), 320),
			})
		}
	}
	return addresses
}

func (s *parseState) inspectHeader(header message.Header, partPath string) error {
	fields := header.Fields()
	s.headers += fields.Len()
	if s.headers > s.limits.MaxHeaders {
		return fmt.Errorf("%w: maximum %d fields", ErrTooManyHeaders, s.limits.MaxHeaders)
	}
	var partBytes int64
	for fields.Next() {
		raw, err := fields.Raw()
		if err != nil {
			return fmt.Errorf("read MIME header in part %s: %w", partPath, err)
		}
		partBytes += int64(len(raw))
		if partBytes > s.limits.MaxHeaderBytes {
			return fmt.Errorf("%w: part %s exceeds %d bytes", ErrHeaderTooLarge, partPath, s.limits.MaxHeaderBytes)
		}
	}
	s.headerBytes += partBytes
	if s.headerBytes > s.limits.MaxTotalHeaderBytes {
		return fmt.Errorf("%w: all parts exceed %d bytes", ErrHeaderTooLarge, s.limits.MaxTotalHeaderBytes)
	}
	return nil
}

func (s *parseState) recoverDecodeError(err error, partPath string) error {
	if err == nil {
		return nil
	}
	switch {
	case message.IsUnknownCharset(err):
		s.warn(WarningUnknownCharset, partPath, "Part uses an unsupported charset")
		return nil
	case message.IsUnknownEncoding(err):
		s.warn(WarningUnknownTransferEncoding, partPath, "Part uses an unsupported transfer encoding")
		return nil
	default:
		return err
	}
}

func (s *parseState) consumeEntity(ctx context.Context, entity *message.Entity, partPath string) error {
	mediaType, typeParams, typeErr := entity.Header.ContentType()
	if typeErr != nil {
		s.warn(WarningMalformedContentType, partPath, "Content-Type could not be parsed")
		mediaType = fallbackMediaType(entity.Header.Get("Content-Type"))
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if strings.HasPrefix(mediaType, "multipart/") {
		return nil
	}

	disposition, dispositionParams, dispositionErr := "", map[string]string(nil), error(nil)
	if rawDisposition := entity.Header.Get("Content-Disposition"); rawDisposition != "" {
		disposition, dispositionParams, dispositionErr = entity.Header.ContentDisposition()
		if dispositionErr != nil {
			s.warn(WarningMalformedDisposition, partPath, "Content-Disposition could not be parsed")
			disposition = fallbackMediaType(rawDisposition)
		}
	}
	disposition = strings.ToLower(strings.TrimSpace(disposition))
	filename := ""
	if dispositionParams != nil {
		filename = dispositionParams["filename"]
	}
	if filename == "" && typeParams != nil {
		filename = typeParams["name"]
	}

	isTextBody := (mediaType == "text/plain" || mediaType == "text/html") && disposition != "attachment" && filename == ""
	captureLimit := 0
	if isTextBody {
		switch mediaType {
		case "text/plain":
			captureLimit = remainingPreviewBytes(s.result.Text, s.limits.MaxTextBytes)
		case "text/html":
			captureLimit = remainingPreviewBytes(string(s.result.HTMLSource), s.limits.MaxHTMLBytes)
		}
	}
	preview, sizeBytes, truncated, err := s.consumeLeaf(ctx, entity.Body, captureLimit)
	if err != nil {
		return fmt.Errorf("consume decoded part %s: %w", partPath, err)
	}

	if isTextBody {
		value := normalizeBodyText(preview)
		switch mediaType {
		case "text/plain":
			s.result.Text = appendPreview(s.result.Text, value, s.limits.MaxTextBytes)
			if truncated {
				s.warn(WarningTextTruncated, partPath, "Text preview exceeded its storage limit")
			}
		case "text/html":
			s.result.HTMLSource = UnsafeHTML(appendPreview(string(s.result.HTMLSource), value, s.limits.MaxHTMLBytes))
			if truncated {
				s.warn(WarningHTMLTruncated, partPath, "HTML source exceeded its storage limit")
			}
		}
		return nil
	}

	safeFilename, normalized := normalizeFilename(filename, s.limits.MaxFilenameBytes)
	if safeFilename == "" {
		safeFilename = "attachment"
	}
	if normalized {
		s.warn(WarningFilenameNormalized, partPath, "Attachment filename required normalization")
	}
	if disposition == "" {
		disposition = "attachment"
	}
	s.result.Attachments = append(s.result.Attachments, Attachment{
		PartPath:    partPath,
		FileName:    safeFilename,
		ContentType: mediaType,
		Disposition: disposition,
		ContentID:   truncateUTF8(cleanHeaderValue(strings.Trim(entity.Header.Get("Content-ID"), "<>")), s.limits.MaxSubjectBytes),
		SizeBytes:   sizeBytes,
	})
	return nil
}

func (s *parseState) consumeLeaf(ctx context.Context, source io.Reader, captureLimit int) ([]byte, int64, bool, error) {
	if source == nil {
		return nil, 0, false, nil
	}
	if captureLimit < 0 {
		captureLimit = 0
	}
	preview := make([]byte, 0, captureLimit)
	buffer := make([]byte, 32<<10)
	var partBytes int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, partBytes, false, err
		}
		read, err := source.Read(buffer)
		if read > 0 {
			partBytes += int64(read)
			s.decodedBytes += int64(read)
			if partBytes > s.limits.MaxPartBytes {
				return nil, partBytes, false, fmt.Errorf("%w: maximum %d bytes", ErrPartTooLarge, s.limits.MaxPartBytes)
			}
			if s.decodedBytes > s.limits.MaxDecodedBytes {
				return nil, partBytes, false, fmt.Errorf("%w: maximum %d bytes", ErrDecodedContentTooLarge, s.limits.MaxDecodedBytes)
			}
			remaining := captureLimit - len(preview)
			if remaining > 0 {
				copyBytes := min(read, remaining)
				preview = append(preview, buffer[:copyBytes]...)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, partBytes, false, err
		}
	}
	return preview, partBytes, partBytes > int64(captureLimit), nil
}

func (s *parseState) warn(code WarningCode, partPath, detail string) {
	warning := Warning{Code: code, PartPath: partPath, Detail: truncateUTF8(detail, 256)}
	if len(s.result.Warnings) < s.limits.MaxWarnings {
		s.result.Warnings = append(s.result.Warnings, warning)
		return
	}
	if s.warningsTruncated {
		return
	}
	s.warningsTruncated = true
	s.result.Warnings[len(s.result.Warnings)-1] = Warning{
		Code:     WarningListTruncated,
		PartPath: "0",
		Detail:   "Additional parser warnings were omitted",
	}
}

func formatPartPath(path []int) string {
	if len(path) == 0 {
		return "0"
	}
	parts := make([]string, len(path))
	for index, value := range path {
		parts[index] = strconv.Itoa(value + 1)
	}
	return strings.Join(parts, ".")
}

func fallbackMediaType(value string) string {
	mediaType, _, _ := strings.Cut(value, ";")
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "" {
		return "application/octet-stream"
	}
	return mediaType
}

func isHeaderLimitError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "header exceeds maximum size")
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(buffer)
}

type countingReader struct {
	source io.Reader
	read   int64
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	read, err := r.source.Read(buffer)
	r.read += int64(read)
	return read, err
}
