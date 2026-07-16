package httpapi

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/google/uuid"

	"mailwisp/internal/mailbox"
	"mailwisp/internal/message"
)

func TestMessageCursorRoundTripAndValidation(t *testing.T) {
	receivedAt := time.Date(2026, 7, 16, 1, 2, 3, 456789123, time.UTC)
	cursor := mailbox.MessageCursor{
		ReceivedAt: receivedAt,
		ID:         message.MessageID("018f26e5-8f04-7b44-8ba2-4a8f434dcb12"),
	}
	encoded, err := encodeMessageCursor(cursor)
	if err != nil {
		t.Fatalf("encodeMessageCursor() error = %v", err)
	}
	decoded, err := decodeMessageCursor(encoded)
	if err != nil {
		t.Fatalf("decodeMessageCursor() error = %v", err)
	}
	if decoded.ID != cursor.ID || !decoded.ReceivedAt.Equal(receivedAt.Truncate(time.Microsecond)) {
		t.Fatalf("decoded cursor = %+v, want %+v", decoded, cursor)
	}
	beforeEpoch := cursor
	beforeEpoch.ReceivedAt = time.Date(1965, 2, 3, 4, 5, 6, 789123456, time.UTC)
	encodedBeforeEpoch, err := encodeMessageCursor(beforeEpoch)
	if err != nil {
		t.Fatalf("encodeMessageCursor(before epoch) error = %v", err)
	}
	decodedBeforeEpoch, err := decodeMessageCursor(encodedBeforeEpoch)
	if err != nil {
		t.Fatalf("decodeMessageCursor(before epoch) error = %v", err)
	}
	if decodedBeforeEpoch.ID != beforeEpoch.ID || !decodedBeforeEpoch.ReceivedAt.Equal(beforeEpoch.ReceivedAt.Truncate(time.Microsecond)) {
		t.Fatalf("decoded before-epoch cursor = %+v, want %+v", decodedBeforeEpoch, beforeEpoch)
	}
	if empty, err := decodeMessageCursor(""); err != nil || empty != nil {
		t.Fatalf("decodeMessageCursor(empty) = %+v, %v", empty, err)
	}

	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	payload[0]++
	invalidVersion := base64.RawURLEncoding.EncodeToString(payload)
	for _, invalid := range []string{"not-base64!", base64.RawURLEncoding.EncodeToString([]byte{1, 2, 3}), invalidVersion} {
		if _, err := decodeMessageCursor(invalid); err == nil {
			t.Fatalf("decodeMessageCursor(%q) succeeded", invalid)
		}
	}
	if _, err := encodeMessageCursor(mailbox.MessageCursor{ReceivedAt: receivedAt, ID: message.MessageID(uuid.NewString())}); err == nil {
		t.Fatal("encodeMessageCursor(v4) succeeded")
	}
	if _, err := encodeMessageCursor(mailbox.MessageCursor{ID: cursor.ID}); err == nil {
		t.Fatal("encodeMessageCursor(zero time) succeeded")
	}
}
