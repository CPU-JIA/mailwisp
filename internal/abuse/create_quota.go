// Package abuse implements persistent, privacy-preserving public resource admission.
package abuse

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"net"
	"time"
)

var (
	// ErrDailyCreateQuotaExceeded indicates that one client identity exhausted its UTC-day allowance.
	ErrDailyCreateQuotaExceeded = errors.New("daily Inbox creation quota exceeded")
	// ErrInvalidClientAddress indicates that a transport supplied a non-IP client identity.
	ErrInvalidClientAddress = errors.New("invalid client IP address")
)

const createIdentityDomain = "mailwisp:create-quota:v1\x00"

// IdentityDigest is the HMAC-SHA-256 identity persisted instead of a plaintext client IP.
type IdentityDigest [sha256.Size]byte

// Repository atomically consumes one daily creation allowance.
type Repository interface {
	ConsumeInboxCreate(context.Context, IdentityDigest, time.Time, int) (int, error)
}

// Decision describes one persistent daily quota outcome.
type Decision struct {
	Limit     int
	Remaining int
	ResetAt   time.Time
}

// Service converts normalized client IPs into private persistent quota identities.
type Service struct {
	repository Repository
	key        []byte
	dailyLimit int
	now        func() time.Time
}

// NewService constructs a persistent Inbox creation quota.
func NewService(repository Repository, key []byte, dailyLimit int) (*Service, error) {
	if repository == nil {
		return nil, errors.New("create quota repository is required")
	}
	if len(key) != 32 {
		return nil, errors.New("create quota HMAC key must contain exactly 32 bytes")
	}
	if dailyLimit <= 0 || dailyLimit > 1_000_000 {
		return nil, errors.New("create quota daily limit must be between 1 and 1000000")
	}
	return &Service{repository: repository, key: append([]byte(nil), key...), dailyLimit: dailyLimit, now: time.Now}, nil
}

// Consume records one create request against the client's current UTC-day bucket.
func (s *Service) Consume(ctx context.Context, clientAddress string) (Decision, error) {
	if ctx == nil {
		return Decision{}, errors.New("create quota context is required")
	}
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	ip := net.ParseIP(clientAddress)
	if ip == nil {
		return Decision{}, ErrInvalidClientAddress
	}
	identity := normalizedIPBytes(ip)
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte(createIdentityDomain))
	_, _ = mac.Write(identity)
	var digest IdentityDigest
	copy(digest[:], mac.Sum(nil))

	now := s.now().UTC()
	bucket := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	used, err := s.repository.ConsumeInboxCreate(ctx, digest, bucket, s.dailyLimit)
	decision := Decision{
		Limit:     s.dailyLimit,
		Remaining: max(0, s.dailyLimit-used),
		ResetAt:   bucket.Add(24 * time.Hour),
	}
	if err != nil {
		return decision, err
	}
	return decision, nil
}

func normalizedIPBytes(ip net.IP) []byte {
	if ipv4 := ip.To4(); ipv4 != nil {
		return ipv4
	}
	return ip.To16()
}
