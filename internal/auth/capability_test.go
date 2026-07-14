package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"mailwisp/internal/message"
)

func TestCapabilityServiceIssuesDigestOnlyCredential(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	inboxID := message.InboxID(uuid.NewString())
	scopes, _ := NewScopeSet(ScopeInboxRead, ScopeMessageRead)
	generated := deterministicToken(t, TokenCapability)
	var created NewCapability
	repository := &capabilityRepositoryStub{create: func(_ context.Context, capability NewCapability) (CapabilityRecord, error) {
		created = capability
		return recordFromNew(capability), nil
	}}
	service := newTestCapabilityService(t, repository, now, generated)

	issued, err := service.Issue(context.Background(), inboxID, scopes, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if issued.Plaintext == "" || issued.KID != generated.KID() || issued.InboxID != inboxID || issued.Scopes != scopes {
		t.Fatalf("Issue() = %+v", issued)
	}
	parsed, err := ParseToken(issued.Plaintext)
	if err != nil || parsed.KID() != created.KID {
		t.Fatalf("issued plaintext ParseToken() = %+v, %v", parsed, err)
	}
	wantDigest, _ := generated.Digest()
	if !EqualDigest(created.Digest, wantDigest) || created.KID == issued.Plaintext {
		t.Fatal("repository did not receive digest-only credential material")
	}
}

func TestCapabilityServiceRetriesKIDCollision(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	inboxID := message.InboxID(uuid.NewString())
	scopes, _ := NewScopeSet(ScopeInboxRead)
	first := deterministicToken(t, TokenCapability)
	second := first
	second.kid = "f" + first.KID()[1:]
	generation := 0
	createCalls := 0
	repository := &capabilityRepositoryStub{create: func(_ context.Context, capability NewCapability) (CapabilityRecord, error) {
		createCalls++
		if createCalls == 1 {
			return CapabilityRecord{}, ErrCapabilityKIDConflict
		}
		return recordFromNew(capability), nil
	}}
	service, err := NewCapabilityService(repository)
	if err != nil {
		t.Fatalf("NewCapabilityService() error = %v", err)
	}
	service.now = func() time.Time { return now }
	service.generate = func(TokenType) (Token, error) {
		generation++
		if generation == 1 {
			return first, nil
		}
		return second, nil
	}

	issued, err := service.Issue(context.Background(), inboxID, scopes, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if createCalls != 2 || issued.KID != second.KID() {
		t.Fatalf("Issue() calls/KID = %d/%q", createCalls, issued.KID)
	}
}

func TestCapabilityServiceAuthenticatesAndDefaultsScopeToDeny(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	token := deterministicToken(t, TokenCapability)
	plaintext, _ := token.Encode()
	digest, _ := token.Digest()
	scopes, _ := NewScopeSet(ScopeInboxRead, ScopeMessageRead)
	record := CapabilityRecord{
		ID: "credential-id", InboxID: message.InboxID(uuid.NewString()), KID: token.KID(), Digest: digest,
		Scopes: scopes, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), InboxActive: true,
	}
	repository := &capabilityRepositoryStub{find: func(_ context.Context, kid string) (CapabilityRecord, error) {
		if kid != token.KID() {
			t.Fatalf("FindCapabilityByKID(%q)", kid)
		}
		return record, nil
	}}
	service := newTestCapabilityService(t, repository, now, token)

	principal, err := service.Authenticate(context.Background(), plaintext, ScopeMessageRead)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if principal.InboxID != record.InboxID || principal.CredentialID != record.ID {
		t.Fatalf("Authenticate() principal = %+v", principal)
	}
	if _, err := service.Authenticate(context.Background(), plaintext, ScopeInboxDelete); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Authenticate(missing scope) error = %v, want ErrForbidden", err)
	}
}

func TestCapabilityServiceUsesUniformAuthenticationFailure(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	token := deterministicToken(t, TokenCapability)
	plaintext, _ := token.Encode()
	digest, _ := token.Digest()
	scopes, _ := NewScopeSet(ScopeInboxRead)
	baseRecord := CapabilityRecord{
		ID: "credential-id", InboxID: message.InboxID(uuid.NewString()), KID: token.KID(), Digest: digest,
		Scopes: scopes, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), InboxActive: true,
	}

	tests := map[string]func() (string, CapabilityRecord, error){
		"malformed":   func() (string, CapabilityRecord, error) { return "not-a-token", baseRecord, nil },
		"unknown kid": func() (string, CapabilityRecord, error) { return plaintext, CapabilityRecord{}, ErrCapabilityNotFound },
		"wrong token type": func() (string, CapabilityRecord, error) {
			personalAccess := deterministicToken(t, TokenPersonalAccess)
			encoded, _ := personalAccess.Encode()
			return encoded, baseRecord, nil
		},
		"wrong secret": func() (string, CapabilityRecord, error) {
			wrong := token
			wrong.secret[0] ^= 0xff
			encoded, _ := wrong.Encode()
			return encoded, baseRecord, nil
		},
		"revoked": func() (string, CapabilityRecord, error) {
			record := baseRecord
			revokedAt := now.Add(-time.Minute)
			record.RevokedAt = &revokedAt
			return plaintext, record, nil
		},
		"expired": func() (string, CapabilityRecord, error) {
			record := baseRecord
			record.ExpiresAt = now
			return plaintext, record, nil
		},
		"inbox disabled": func() (string, CapabilityRecord, error) {
			record := baseRecord
			record.InboxActive = false
			return plaintext, record, nil
		},
		"inbox expired": func() (string, CapabilityRecord, error) {
			record := baseRecord
			expiresAt := now
			record.InboxExpiresAt = &expiresAt
			return plaintext, record, nil
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			input, record, findErr := test()
			repository := &capabilityRepositoryStub{find: func(context.Context, string) (CapabilityRecord, error) {
				return record, findErr
			}}
			service := newTestCapabilityService(t, repository, now, token)
			if _, err := service.Authenticate(context.Background(), input); !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("Authenticate() error = %v, want ErrUnauthenticated", err)
			}
		})
	}
}

func TestCapabilityServicePreservesRepositoryOutage(t *testing.T) {
	t.Parallel()

	token := deterministicToken(t, TokenCapability)
	plaintext, _ := token.Encode()
	repositoryErr := errors.New("database unavailable")
	repository := &capabilityRepositoryStub{find: func(context.Context, string) (CapabilityRecord, error) {
		return CapabilityRecord{}, repositoryErr
	}}
	service := newTestCapabilityService(t, repository, time.Now().UTC(), token)
	if _, err := service.Authenticate(context.Background(), plaintext); !errors.Is(err, repositoryErr) || errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("Authenticate() error = %v", err)
	}
}

func TestCapabilityServiceRotatesAndRevokes(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	oldToken := deterministicToken(t, TokenCapability)
	oldPlaintext, _ := oldToken.Encode()
	oldDigest, _ := oldToken.Digest()
	newToken := oldToken
	newToken.kid = "f" + oldToken.KID()[1:]
	newToken.secret[0] ^= 0x7f
	scopes, _ := NewScopeSet(ScopeInboxRead, ScopeMessageRead)
	oldRecord := CapabilityRecord{
		ID: "old-id", InboxID: message.InboxID(uuid.NewString()), KID: oldToken.KID(), Digest: oldDigest,
		Scopes: scopes, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), InboxActive: true,
	}
	var replacement ReplacementCapability
	var revokedID CredentialID
	repository := &capabilityRepositoryStub{
		find: func(context.Context, string) (CapabilityRecord, error) { return oldRecord, nil },
		rotate: func(_ context.Context, id CredentialID, next ReplacementCapability, _ time.Time) (CapabilityRecord, error) {
			if id != oldRecord.ID {
				t.Fatalf("RotateCapability() id = %q", id)
			}
			replacement = next
			return CapabilityRecord{
				ID: "new-id", InboxID: oldRecord.InboxID, KID: next.KID, Digest: next.Digest,
				Scopes: oldRecord.Scopes, CreatedAt: next.CreatedAt, ExpiresAt: oldRecord.ExpiresAt, InboxActive: true,
			}, nil
		},
		revoke: func(_ context.Context, id CredentialID, _ time.Time) error {
			revokedID = id
			return nil
		},
	}
	service := newTestCapabilityService(t, repository, now, newToken)
	rotated, err := service.Rotate(context.Background(), oldPlaintext)
	if err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}
	if rotated.ID != "new-id" || rotated.KID != newToken.KID() || replacement.KID != newToken.KID() || rotated.ExpiresAt != oldRecord.ExpiresAt {
		t.Fatalf("Rotate() = %+v, replacement = %+v", rotated, replacement)
	}
	if err := service.Revoke(context.Background(), oldPlaintext); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if revokedID != oldRecord.ID {
		t.Fatalf("RevokeCapability() ID = %q", revokedID)
	}
}

func TestCapabilityServiceValidatesIssueInputs(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	service := newTestCapabilityService(t, &capabilityRepositoryStub{}, now, deterministicToken(t, TokenCapability))
	scopes, _ := NewScopeSet(ScopeInboxRead)
	if _, err := service.Issue(context.Background(), "invalid", scopes, now.Add(time.Hour)); !errors.Is(err, ErrCapabilitySubjectUnavailable) {
		t.Fatalf("Issue(invalid Inbox) error = %v", err)
	}
	if _, err := service.Issue(context.Background(), message.InboxID(uuid.NewString()), scopes, now); !errors.Is(err, ErrCapabilityLifetime) {
		t.Fatalf("Issue(expired) error = %v", err)
	}
	if _, err := service.Issue(context.Background(), message.InboxID(uuid.NewString()), 0, now.Add(time.Hour)); err == nil {
		t.Fatal("Issue(empty scopes) error = nil")
	}
	if _, err := NewCapabilityService(nil); err == nil {
		t.Fatal("NewCapabilityService(nil) error = nil")
	}
}

func newTestCapabilityService(t testing.TB, repository CapabilityRepository, now time.Time, generated Token) *CapabilityService {
	t.Helper()
	service, err := NewCapabilityService(repository)
	if err != nil {
		t.Fatalf("NewCapabilityService() error = %v", err)
	}
	service.now = func() time.Time { return now }
	service.generate = func(tokenType TokenType) (Token, error) {
		if tokenType != TokenCapability {
			t.Fatalf("generate token type = %q", tokenType)
		}
		return generated, nil
	}
	return service
}

func recordFromNew(capability NewCapability) CapabilityRecord {
	return CapabilityRecord{
		ID: "credential-id", InboxID: capability.InboxID, KID: capability.KID, Digest: capability.Digest,
		Scopes: capability.Scopes, CreatedAt: capability.CreatedAt, ExpiresAt: capability.ExpiresAt, InboxActive: true,
	}
}

type capabilityRepositoryStub struct {
	create func(context.Context, NewCapability) (CapabilityRecord, error)
	find   func(context.Context, string) (CapabilityRecord, error)
	rotate func(context.Context, CredentialID, ReplacementCapability, time.Time) (CapabilityRecord, error)
	revoke func(context.Context, CredentialID, time.Time) error
}

func (s *capabilityRepositoryStub) CreateCapability(ctx context.Context, capability NewCapability) (CapabilityRecord, error) {
	if s.create == nil {
		return CapabilityRecord{}, errors.New("unexpected CreateCapability call")
	}
	return s.create(ctx, capability)
}

func (s *capabilityRepositoryStub) FindCapabilityByKID(ctx context.Context, kid string) (CapabilityRecord, error) {
	if s.find == nil {
		return CapabilityRecord{}, errors.New("unexpected FindCapabilityByKID call")
	}
	return s.find(ctx, kid)
}

func (s *capabilityRepositoryStub) RotateCapability(ctx context.Context, current CredentialID, replacement ReplacementCapability, at time.Time) (CapabilityRecord, error) {
	if s.rotate == nil {
		return CapabilityRecord{}, errors.New("unexpected RotateCapability call")
	}
	return s.rotate(ctx, current, replacement, at)
}

func (s *capabilityRepositoryStub) RevokeCapability(ctx context.Context, id CredentialID, at time.Time) error {
	if s.revoke == nil {
		return errors.New("unexpected RevokeCapability call")
	}
	return s.revoke(ctx, id, at)
}
