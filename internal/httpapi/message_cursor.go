package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"time"

	"github.com/google/uuid"

	"mailwisp/internal/mailbox"
	"mailwisp/internal/message"
)

const (
	messageCursorVersion = byte(1)
	messageCursorBytes   = 1 + 8 + 16
)

var (
	minimumCursorTime = time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	maximumCursorTime = time.Date(9999, 12, 31, 23, 59, 59, 999999000, time.UTC)
)

func encodeMessageCursor(cursor mailbox.MessageCursor) (string, error) {
	identifier, err := uuid.Parse(string(cursor.ID))
	if err != nil || identifier.Version() != 7 {
		return "", errors.New("message cursor ID is invalid")
	}
	receivedAt := cursor.ReceivedAt.UTC().Truncate(time.Microsecond)
	if receivedAt.IsZero() || receivedAt.Before(minimumCursorTime) || receivedAt.After(maximumCursorTime) {
		return "", errors.New("message cursor time is invalid")
	}
	payload := make([]byte, messageCursorBytes)
	payload[0] = messageCursorVersion
	binary.BigEndian.PutUint64(payload[1:9], uint64(receivedAt.UnixMicro()))
	copy(payload[9:], identifier[:])
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeMessageCursor(raw string) (*mailbox.MessageCursor, error) {
	if raw == "" {
		return nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(payload) != messageCursorBytes || payload[0] != messageCursorVersion {
		return nil, errors.New("message cursor is invalid")
	}
	var unixMicros int64
	if err := binary.Read(bytes.NewReader(payload[1:9]), binary.BigEndian, &unixMicros); err != nil {
		return nil, errors.New("message cursor time is invalid")
	}
	receivedAt := time.UnixMicro(unixMicros).UTC()
	if receivedAt.IsZero() || receivedAt.Before(minimumCursorTime) || receivedAt.After(maximumCursorTime) {
		return nil, errors.New("message cursor time is invalid")
	}
	identifier, err := uuid.FromBytes(payload[9:])
	if err != nil || identifier.Version() != 7 {
		return nil, errors.New("message cursor ID is invalid")
	}
	return &mailbox.MessageCursor{ReceivedAt: receivedAt, ID: message.MessageID(identifier.String())}, nil
}
