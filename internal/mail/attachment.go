package mail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	message "github.com/emersion/go-message"
)

var errAttachmentFound = errors.New("attachment found")

// OpenAttachment locates one MIME leaf by its stable PartPath and returns a
// bounded decoded stream. The caller owns the returned stream and must close
// it; closing also closes the supplied Raw source.
func (p *Parser) OpenAttachment(ctx context.Context, source io.ReadCloser, partPath string) (io.ReadCloser, error) {
	if ctx == nil {
		return nil, errors.New("attachment context is required")
	}
	if source == nil {
		return nil, errors.New("attachment source is required")
	}
	partPath = strings.TrimSpace(partPath)
	if partPath == "" {
		_ = source.Close()
		return nil, errors.New("attachment part path is required")
	}
	limited := &io.LimitedReader{R: &contextReader{ctx: ctx, source: source}, N: p.limits.MaxRawBytes + 1}
	counted := &countingReader{source: limited}
	entity, readErr := message.ReadWithOptions(counted, &message.ReadOptions{MaxHeaderBytes: p.limits.MaxHeaderBytes})
	if entity == nil {
		_ = source.Close()
		return nil, fmt.Errorf("read MIME root: %w", readErr)
	}
	var stream io.Reader
	walkErr := entity.Walk(func(path []int, part *message.Entity, decodeErr error) error {
		currentPath := formatPartPath(path)
		if currentPath != partPath {
			return nil
		}
		if decodeErr != nil && !message.IsUnknownCharset(decodeErr) && !message.IsUnknownEncoding(decodeErr) {
			return decodeErr
		}
		mediaType, _, typeErr := part.Header.ContentType()
		if typeErr != nil || strings.HasPrefix(strings.ToLower(strings.TrimSpace(mediaType)), "multipart/") {
			return errors.New("attachment part is not a leaf MIME entity")
		}
		stream = part.Body
		return errAttachmentFound
	})
	if !errors.Is(walkErr, errAttachmentFound) || stream == nil {
		_ = source.Close()
		if walkErr != nil && !errors.Is(walkErr, errAttachmentFound) {
			return nil, fmt.Errorf("locate MIME attachment: %w", walkErr)
		}
		return nil, errors.New("attachment part not found")
	}
	return &boundedAttachmentReader{ctx: ctx, source: stream, raw: source, maxBytes: p.limits.MaxPartBytes}, nil
}

type boundedAttachmentReader struct {
	ctx      context.Context
	source   io.Reader
	raw      io.Closer
	maxBytes int64
	read     int64
	closed   bool
}

func (r *boundedAttachmentReader) Read(buffer []byte) (int, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	if r.read >= r.maxBytes {
		probe := make([]byte, 1)
		n, err := r.source.Read(probe)
		if n > 0 {
			return 0, fmt.Errorf("%w: maximum %d bytes", ErrPartTooLarge, r.maxBytes)
		}
		return 0, err
	}
	remaining := r.maxBytes - r.read
	if int64(len(buffer)) > remaining {
		buffer = buffer[:remaining]
	}
	n, err := r.source.Read(buffer)
	r.read += int64(n)
	return n, err
}

func (r *boundedAttachmentReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	return r.raw.Close()
}
