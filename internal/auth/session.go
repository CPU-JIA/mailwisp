package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"mailwisp/internal/message"
)

const (
	browserSessionVersion = 1
	csrfBytes             = 32
	sessionPlaintextBytes = 1 + 16 + 8 + 4 + sha256.Size
	csrfDigestDomain      = "mailwisp-browser-csrf-v1\x00"
)

var (
	// ErrBrowserSessionDisabled indicates that no browser session key was configured.
	ErrBrowserSessionDisabled = errors.New("browser session is disabled")
	// ErrCSRF indicates a missing or mismatched browser CSRF proof.
	ErrCSRF = errors.New("browser CSRF verification failed")
)

// BrowserSession contains encrypted Cookie material and the double-submit CSRF value.
type BrowserSession struct {
	CookieValue string
	CSRFToken   string
	ExpiresAt   time.Time
}

// BrowserSessionManager creates authenticated, short-lived browser sessions.
// The encrypted Cookie never contains a reusable Capability plaintext.
type BrowserSessionManager struct {
	aead     cipher.AEAD
	lifetime time.Duration
	now      func() time.Time
	random   io.Reader
}

// NewBrowserSessionManager constructs AES-256-GCM browser sessions.
func NewBrowserSessionManager(key []byte, lifetime time.Duration) (*BrowserSessionManager, error) {
	if len(key) == 0 {
		return nil, ErrBrowserSessionDisabled
	}
	if len(key) != 32 {
		return nil, errors.New("browser session key must contain exactly 32 bytes")
	}
	if lifetime <= 0 || lifetime > 7*24*time.Hour {
		return nil, errors.New("browser session lifetime must be between 1ns and 168h")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create browser session cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create browser session AEAD: %w", err)
	}
	return &BrowserSessionManager{aead: aead, lifetime: lifetime, now: time.Now, random: rand.Reader}, nil
}

// Issue exchanges one already-authenticated Capability principal for a browser session.
func (m *BrowserSessionManager) Issue(ctx context.Context, principal Principal) (BrowserSession, error) {
	if ctx == nil {
		return BrowserSession{}, ErrUnauthenticated
	}
	if err := ctx.Err(); err != nil {
		return BrowserSession{}, err
	}
	inboxUUID, err := uuid.Parse(string(principal.InboxID))
	if err != nil || principal.Scopes.Mask() == 0 {
		return BrowserSession{}, ErrUnauthenticated
	}
	now := m.now().UTC()
	expiresAt := now.Add(m.lifetime)
	if principal.ExpiresAt.IsZero() || !now.Before(principal.ExpiresAt.UTC()) {
		return BrowserSession{}, ErrUnauthenticated
	}
	if principal.ExpiresAt.UTC().Before(expiresAt) {
		expiresAt = principal.ExpiresAt.UTC()
	}
	csrf := make([]byte, csrfBytes)
	if _, err := io.ReadFull(m.random, csrf); err != nil {
		return BrowserSession{}, fmt.Errorf("generate browser CSRF token: %w", err)
	}
	plaintext := make([]byte, sessionPlaintextBytes)
	plaintext[0] = browserSessionVersion
	copy(plaintext[1:17], inboxUUID[:])
	binary.BigEndian.PutUint64(plaintext[17:25], uint64(expiresAt.Unix()))
	binary.BigEndian.PutUint32(plaintext[25:29], principal.Scopes.Mask())
	digest := digestCSRF(csrf)
	copy(plaintext[29:], digest[:])
	nonce := make([]byte, m.aead.NonceSize())
	if _, err := io.ReadFull(m.random, nonce); err != nil {
		return BrowserSession{}, fmt.Errorf("generate browser session nonce: %w", err)
	}
	sealed := m.aead.Seal(nonce, nonce, plaintext, nil)
	return BrowserSession{
		CookieValue: base64.RawURLEncoding.EncodeToString(sealed),
		CSRFToken:   base64.RawURLEncoding.EncodeToString(csrf),
		ExpiresAt:   expiresAt,
	}, nil
}

// Authenticate decrypts one browser Cookie and enforces optional CSRF and scopes.
func (m *BrowserSessionManager) Authenticate(ctx context.Context, cookieValue, csrfToken string, requireCSRF bool, required ...Scope) (Principal, error) {
	if ctx == nil || cookieValue == "" {
		return Principal{}, ErrUnauthenticated
	}
	if err := ctx.Err(); err != nil {
		return Principal{}, err
	}
	sealed, err := base64.RawURLEncoding.Strict().DecodeString(cookieValue)
	if err != nil || len(sealed) <= m.aead.NonceSize() {
		return Principal{}, ErrUnauthenticated
	}
	nonce := sealed[:m.aead.NonceSize()]
	plaintext, err := m.aead.Open(nil, nonce, sealed[m.aead.NonceSize():], nil)
	if err != nil || len(plaintext) != sessionPlaintextBytes || plaintext[0] != browserSessionVersion {
		return Principal{}, ErrUnauthenticated
	}
	expiresAt := time.Unix(int64(binary.BigEndian.Uint64(plaintext[17:25])), 0).UTC()
	if !m.now().UTC().Before(expiresAt) {
		return Principal{}, ErrUnauthenticated
	}
	scopes, err := ScopeSetFromMask(binary.BigEndian.Uint32(plaintext[25:29]))
	if err != nil || !scopes.Has(required...) {
		if err == nil {
			return Principal{}, ErrForbidden
		}
		return Principal{}, ErrUnauthenticated
	}
	if requireCSRF {
		csrf, err := base64.RawURLEncoding.Strict().DecodeString(csrfToken)
		if err != nil || len(csrf) != csrfBytes {
			return Principal{}, ErrCSRF
		}
		digest := digestCSRF(csrf)
		if subtle.ConstantTimeCompare(digest[:], plaintext[29:]) != 1 {
			return Principal{}, ErrCSRF
		}
	}
	var inboxUUID uuid.UUID
	copy(inboxUUID[:], plaintext[1:17])
	return Principal{
		InboxID:   message.InboxID(inboxUUID.String()),
		KID:       "browser-session",
		Scopes:    scopes,
		ExpiresAt: expiresAt,
	}, nil
}

func digestCSRF(csrf []byte) [sha256.Size]byte {
	hash := sha256.New()
	_, _ = hash.Write([]byte(csrfDigestDomain))
	_, _ = hash.Write(csrf)
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}
