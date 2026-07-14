package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"mailwisp/internal/message"
)

const maxKIDGenerationAttempts = 3

var (
	// ErrUnauthenticated is returned for every invalid, unknown, revoked, expired, or inactive credential.
	ErrUnauthenticated = errors.New("authentication failed")
	// ErrForbidden indicates that a valid principal lacks a required scope.
	ErrForbidden = errors.New("insufficient capability scope")
	// ErrCapabilityNotFound indicates that a KID or credential ID is absent from persistence.
	ErrCapabilityNotFound = errors.New("capability credential not found")
	// ErrCapabilityKIDConflict indicates an extremely unlikely random KID collision.
	ErrCapabilityKIDConflict = errors.New("capability KID conflict")
	// ErrCapabilitySubjectUnavailable indicates that the target Inbox cannot receive a capability.
	ErrCapabilitySubjectUnavailable = errors.New("capability Inbox is unavailable")
	// ErrCapabilityLifetime indicates an invalid or resource-exceeding expiration.
	ErrCapabilityLifetime = errors.New("invalid capability lifetime")
	// ErrCapabilityAlreadyRotated indicates that another transaction already replaced the credential.
	ErrCapabilityAlreadyRotated = errors.New("capability already rotated")
)

// CredentialID identifies one persisted capability credential.
type CredentialID string

// NewCapability contains digest-only material for persistence.
type NewCapability struct {
	InboxID   message.InboxID
	KID       string
	Digest    Digest
	Scopes    ScopeSet
	CreatedAt time.Time
	ExpiresAt time.Time
}

// ReplacementCapability contains new secret material for an atomic rotation.
// Inbox, scopes, and expiration are copied from the locked current record.
type ReplacementCapability struct {
	KID       string
	Digest    Digest
	CreatedAt time.Time
}

// CapabilityRecord is the non-plaintext state required for authentication.
type CapabilityRecord struct {
	ID             CredentialID
	InboxID        message.InboxID
	KID            string
	Digest         Digest
	Scopes         ScopeSet
	CreatedAt      time.Time
	ExpiresAt      time.Time
	LastUsedAt     *time.Time
	RevokedAt      *time.Time
	InboxActive    bool
	InboxExpiresAt *time.Time
}

// CapabilityRepository persists and atomically rotates Inbox capabilities.
type CapabilityRepository interface {
	CreateCapability(context.Context, NewCapability) (CapabilityRecord, error)
	FindCapabilityByKID(context.Context, string) (CapabilityRecord, error)
	RotateCapability(context.Context, CredentialID, ReplacementCapability, time.Time) (CapabilityRecord, error)
	RevokeCapability(context.Context, CredentialID, time.Time) error
}

// IssuedCapability contains plaintext exactly once for an issuance response.
type IssuedCapability struct {
	ID        CredentialID
	InboxID   message.InboxID
	KID       string
	Plaintext string
	Scopes    ScopeSet
	ExpiresAt time.Time
}

// Principal is the authenticated, Inbox-bound authorization context.
type Principal struct {
	CredentialID CredentialID
	InboxID      message.InboxID
	KID          string
	Scopes       ScopeSet
	ExpiresAt    time.Time
}

// CapabilityService issues and validates passwordless Inbox capabilities.
type CapabilityService struct {
	repository CapabilityRepository
	now        func() time.Time
	generate   func(TokenType) (Token, error)
}

// NewCapabilityService constructs an Inbox capability service.
func NewCapabilityService(repository CapabilityRepository) (*CapabilityService, error) {
	if repository == nil {
		return nil, errors.New("capability repository is required")
	}
	return &CapabilityService{repository: repository, now: time.Now, generate: GenerateToken}, nil
}

// Issue creates a capability bound to one active Inbox and explicit scopes.
func (s *CapabilityService) Issue(ctx context.Context, inboxID message.InboxID, scopes ScopeSet, expiresAt time.Time) (IssuedCapability, error) {
	if ctx == nil {
		return IssuedCapability{}, errors.New("capability issue context is required")
	}
	if _, err := uuid.Parse(string(inboxID)); err != nil {
		return IssuedCapability{}, fmt.Errorf("%w: Inbox ID is invalid", ErrCapabilitySubjectUnavailable)
	}
	if _, err := ScopeSetFromMask(scopes.Mask()); err != nil {
		return IssuedCapability{}, err
	}
	now := s.now().UTC()
	if expiresAt.IsZero() || !expiresAt.After(now) {
		return IssuedCapability{}, ErrCapabilityLifetime
	}
	return s.issue(ctx, NewCapability{InboxID: inboxID, Scopes: scopes, CreatedAt: now, ExpiresAt: expiresAt.UTC()})
}

// Authenticate validates plaintext and enforces every required scope.
func (s *CapabilityService) Authenticate(ctx context.Context, plaintext string, required ...Scope) (Principal, error) {
	record, err := s.resolve(ctx, plaintext)
	if err != nil {
		return Principal{}, err
	}
	if !record.Scopes.Has(required...) {
		return Principal{}, ErrForbidden
	}
	return principalFromRecord(record), nil
}

// Rotate atomically revokes one valid capability and returns a replacement
// with the same Inbox, scopes, and expiration.
func (s *CapabilityService) Rotate(ctx context.Context, plaintext string) (IssuedCapability, error) {
	record, err := s.resolve(ctx, plaintext)
	if err != nil {
		return IssuedCapability{}, err
	}
	for range maxKIDGenerationAttempts {
		token, err := s.generate(TokenCapability)
		if err != nil {
			return IssuedCapability{}, fmt.Errorf("generate replacement capability: %w", err)
		}
		digest, err := token.Digest()
		if err != nil {
			return IssuedCapability{}, err
		}
		createdAt := s.now().UTC()
		newRecord, err := s.repository.RotateCapability(ctx, record.ID, ReplacementCapability{
			KID: token.KID(), Digest: digest, CreatedAt: createdAt,
		}, createdAt)
		if errors.Is(err, ErrCapabilityKIDConflict) {
			continue
		}
		if err != nil {
			return IssuedCapability{}, fmt.Errorf("rotate capability: %w", err)
		}
		encoded, err := token.Encode()
		if err != nil {
			return IssuedCapability{}, err
		}
		return issuedCapability(newRecord, encoded), nil
	}
	return IssuedCapability{}, ErrCapabilityKIDConflict
}

// Revoke immediately invalidates one currently valid capability.
func (s *CapabilityService) Revoke(ctx context.Context, plaintext string) error {
	record, err := s.resolve(ctx, plaintext)
	if err != nil {
		return err
	}
	if err := s.repository.RevokeCapability(ctx, record.ID, s.now().UTC()); err != nil {
		return fmt.Errorf("revoke capability: %w", err)
	}
	return nil
}

func (s *CapabilityService) issue(ctx context.Context, capability NewCapability) (IssuedCapability, error) {
	for range maxKIDGenerationAttempts {
		token, err := s.generate(TokenCapability)
		if err != nil {
			return IssuedCapability{}, fmt.Errorf("generate capability: %w", err)
		}
		digest, err := token.Digest()
		if err != nil {
			return IssuedCapability{}, err
		}
		capability.KID = token.KID()
		capability.Digest = digest
		record, err := s.repository.CreateCapability(ctx, capability)
		if errors.Is(err, ErrCapabilityKIDConflict) {
			continue
		}
		if err != nil {
			return IssuedCapability{}, fmt.Errorf("persist capability: %w", err)
		}
		encoded, err := token.Encode()
		if err != nil {
			return IssuedCapability{}, err
		}
		return issuedCapability(record, encoded), nil
	}
	return IssuedCapability{}, ErrCapabilityKIDConflict
}

func (s *CapabilityService) resolve(ctx context.Context, plaintext string) (CapabilityRecord, error) {
	if ctx == nil {
		return CapabilityRecord{}, ErrUnauthenticated
	}
	token, err := ParseToken(plaintext)
	if err != nil || token.Type() != TokenCapability {
		return CapabilityRecord{}, ErrUnauthenticated
	}
	record, err := s.repository.FindCapabilityByKID(ctx, token.KID())
	if errors.Is(err, ErrCapabilityNotFound) {
		return CapabilityRecord{}, ErrUnauthenticated
	}
	if err != nil {
		return CapabilityRecord{}, fmt.Errorf("lookup capability: %w", err)
	}
	digest, err := token.Digest()
	if err != nil || !EqualDigest(digest, record.Digest) {
		return CapabilityRecord{}, ErrUnauthenticated
	}
	now := s.now().UTC()
	if record.RevokedAt != nil || !now.Before(record.ExpiresAt) || !record.InboxActive {
		return CapabilityRecord{}, ErrUnauthenticated
	}
	if record.InboxExpiresAt != nil && !now.Before(record.InboxExpiresAt.UTC()) {
		return CapabilityRecord{}, ErrUnauthenticated
	}
	return record, nil
}

func principalFromRecord(record CapabilityRecord) Principal {
	return Principal{
		CredentialID: record.ID,
		InboxID:      record.InboxID,
		KID:          record.KID,
		Scopes:       record.Scopes,
		ExpiresAt:    record.ExpiresAt,
	}
}

func issuedCapability(record CapabilityRecord, plaintext string) IssuedCapability {
	return IssuedCapability{
		ID: record.ID, InboxID: record.InboxID, KID: record.KID, Plaintext: plaintext,
		Scopes: record.Scopes, ExpiresAt: record.ExpiresAt,
	}
}
