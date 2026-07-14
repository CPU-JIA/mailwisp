package message

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDeliveryValidate(t *testing.T) {
	t.Parallel()

	validInbox := InboxID("018f26e5-8f04-7b44-8ba2-4a8f434dcb12")
	valid := Delivery{
		Content:        ContentRef{Key: testContentKey, SizeBytes: 10},
		EnvelopeSender: "sender@example.com",
		Recipients:     []InboxID{validInbox},
		ReceivedAt:     time.Now(),
	}

	tests := []struct {
		name     string
		mutate   func(*Delivery)
		wantFail bool
	}{
		{name: "valid"},
		{name: "empty bounce sender is valid", mutate: func(delivery *Delivery) { delivery.EnvelopeSender = "" }},
		{name: "missing content key", mutate: func(delivery *Delivery) { delivery.Content.Key = " " }, wantFail: true},
		{name: "short content digest", mutate: func(delivery *Delivery) { delivery.Content.Key = "sha256/abc" }, wantFail: true},
		{name: "uppercase content digest", mutate: func(delivery *Delivery) {
			delivery.Content.Key = "sha256/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		}, wantFail: true},
		{name: "negative content size", mutate: func(delivery *Delivery) { delivery.Content.SizeBytes = -1 }, wantFail: true},
		{name: "sender too long", mutate: func(delivery *Delivery) { delivery.EnvelopeSender = strings.Repeat("x", 321) }, wantFail: true},
		{name: "missing received time", mutate: func(delivery *Delivery) { delivery.ReceivedAt = time.Time{} }, wantFail: true},
		{name: "missing recipients", mutate: func(delivery *Delivery) { delivery.Recipients = nil }, wantFail: true},
		{name: "invalid recipient", mutate: func(delivery *Delivery) { delivery.Recipients = []InboxID{"not-a-uuid"} }, wantFail: true},
		{name: "duplicate recipient", mutate: func(delivery *Delivery) { delivery.Recipients = []InboxID{validInbox, validInbox} }, wantFail: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			delivery := valid
			delivery.Recipients = append([]InboxID(nil), valid.Recipients...)
			if test.mutate != nil {
				test.mutate(&delivery)
			}
			err := delivery.Validate()
			if test.wantFail && !errors.Is(err, ErrInvalidDelivery) {
				t.Fatalf("Validate() error = %v, want ErrInvalidDelivery", err)
			}
			if !test.wantFail && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

const testContentKey = "sha256/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
