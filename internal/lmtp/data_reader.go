package lmtp

import (
	"bufio"
	"errors"
	"io"
)

var (
	// ErrDataTooLarge indicates that decoded SMTP DATA exceeded the configured limit.
	ErrDataTooLarge = errors.New("LMTP DATA exceeds configured byte limit")
	// ErrDataLineTooLong indicates that one DATA line exceeded the configured limit.
	ErrDataLineTooLong = errors.New("LMTP DATA line exceeds configured byte limit")
	// ErrCommandLineTooLong indicates that one LMTP command exceeded the configured limit.
	ErrCommandLineTooLong = errors.New("LMTP command line exceeds configured byte limit")
)

type dataReader struct {
	reader       *bufio.Reader
	maxBytes     int64
	maxLineBytes int
	written      int64
	pending      []byte
	ended        bool
	terminalErr  error
}

func newDataReader(reader *bufio.Reader, maxBytes int64, maxLineBytes int) *dataReader {
	return &dataReader{
		reader:       reader,
		maxBytes:     maxBytes,
		maxLineBytes: maxLineBytes,
	}
}

func (r *dataReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if len(r.pending) > 0 {
		copied := copy(buffer, r.pending)
		r.pending = r.pending[copied:]
		return copied, nil
	}
	if r.terminalErr != nil {
		return 0, r.terminalErr
	}
	if r.ended {
		return 0, io.EOF
	}

	line, err := readLimitedLine(r.reader, r.maxLineBytes)
	if err != nil {
		if errors.Is(err, ErrCommandLineTooLong) {
			r.terminalErr = ErrDataLineTooLong
			return 0, r.terminalErr
		}
		r.terminalErr = err
		return 0, err
	}
	if line == "." {
		r.ended = true
		return 0, io.EOF
	}
	if len(line) >= 2 && line[0] == '.' && line[1] == '.' {
		line = line[1:]
	}

	decodedBytes := int64(len(line) + 2)
	if decodedBytes > r.maxBytes-r.written {
		r.terminalErr = ErrDataTooLarge
		return 0, r.terminalErr
	}
	r.written += decodedBytes
	r.pending = make([]byte, 0, len(line)+2)
	r.pending = append(r.pending, line...)
	r.pending = append(r.pending, '\r', '\n')

	copied := copy(buffer, r.pending)
	r.pending = r.pending[copied:]
	return copied, nil
}

func (r *dataReader) drain() error {
	if r.ended {
		return nil
	}
	for {
		line, err := readLimitedLine(r.reader, r.maxLineBytes)
		if errors.Is(err, ErrCommandLineTooLong) {
			continue
		}
		if err != nil {
			return err
		}
		if line == "." {
			r.ended = true
			return nil
		}
	}
}

func readLimitedLine(reader *bufio.Reader, limit int) (string, error) {
	buffer := make([]byte, 0, min(limit, 256))
	overLimit := false
	for {
		fragment, continued, err := reader.ReadLine()
		if err != nil {
			return "", err
		}
		if !overLimit {
			if len(buffer)+len(fragment) > limit {
				overLimit = true
			} else {
				buffer = append(buffer, fragment...)
			}
		}
		if !continued {
			if overLimit {
				return "", ErrCommandLineTooLong
			}
			return string(buffer), nil
		}
	}
}
