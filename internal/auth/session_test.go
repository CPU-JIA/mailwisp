package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"mailwisp/internal/message"
)

func TestBrowserSessionRoundTripAndCSRF(t *testing.T) {
	manager, err := NewBrowserSessionManager([]byte(strings.Repeat("k", 32)), 12*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 15, 5, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	manager.random = strings.NewReader(strings.Repeat("r", 128))
	scopes, _ := NewScopeSet(ScopeInboxRead, ScopeInboxDelete)
	session, err := manager.Issue(context.Background(), Principal{
		InboxID:   message.InboxID("018f26e5-8f04-7b44-8ba2-4a8f434dcb12"),
		Scopes:    scopes,
		ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !session.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("ExpiresAt = %v", session.ExpiresAt)
	}
	principal, err := manager.Authenticate(context.Background(), session.CookieValue, session.CSRFToken, true, ScopeInboxDelete)
	if err != nil || principal.InboxID != "018f26e5-8f04-7b44-8ba2-4a8f434dcb12" {
		t.Fatalf("Authenticate() = %+v, %v", principal, err)
	}
	if _, err := manager.Authenticate(context.Background(), session.CookieValue, "wrong", true, ScopeInboxRead); !errors.Is(err, ErrCSRF) {
		t.Fatalf("wrong CSRF error = %v", err)
	}
	if _, err := manager.Authenticate(context.Background(), session.CookieValue, session.CSRFToken, true, ScopeMessageRead); !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing scope error = %v", err)
	}
}

func TestBrowserSessionRejectsTamperingAndExpiry(t *testing.T) {
	manager, err := NewBrowserSessionManager([]byte(strings.Repeat("k", 32)), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 15, 5, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	manager.random = strings.NewReader(strings.Repeat("r", 128))
	scopes, _ := NewScopeSet(ScopeInboxRead)
	session, err := manager.Issue(context.Background(), Principal{
		InboxID: "018f26e5-8f04-7b44-8ba2-4a8f434dcb12", Scopes: scopes, ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	tampered := session.CookieValue[:len(session.CookieValue)-1] + "A"
	if _, err := manager.Authenticate(context.Background(), tampered, "", false, ScopeInboxRead); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("tampered error = %v", err)
	}
	manager.now = func() time.Time { return now.Add(time.Hour) }
	if _, err := manager.Authenticate(context.Background(), session.CookieValue, "", false, ScopeInboxRead); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired error = %v", err)
	}
}
