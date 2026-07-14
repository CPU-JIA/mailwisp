// Package auth implements MailWisp credential grammar and authorization semantics.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	tokenPrefix        = "wisp_"
	tokenVersion       = "v1"
	kidBytes           = 12
	kidEncodedBytes    = kidBytes * 2
	secretBytes        = 32
	secretEncodedBytes = 43
	digestDomain       = "mailwisp-token-v1\x00"
)

var (
	// ErrInvalidToken indicates that a plaintext token does not match the canonical grammar.
	ErrInvalidToken = errors.New("invalid MailWisp token")
)

// TokenType identifies the intended credential protocol.
type TokenType string

const (
	TokenPersonalAccess TokenType = "pat"
	TokenCapability     TokenType = "cap"
	TokenSession        TokenType = "ses"
	TokenWebhookSecret  TokenType = "whsec"
)

// Digest is the domain-separated SHA-256 value stored instead of token plaintext.
type Digest [sha256.Size]byte

// Token contains parsed secret material. It deliberately does not implement
// fmt.Stringer so generic structured logging cannot reveal plaintext.
type Token struct {
	tokenType TokenType
	kid       string
	secret    [secretBytes]byte
}

// GenerateToken creates a canonical token using crypto/rand.
func GenerateToken(tokenType TokenType) (Token, error) {
	return generateToken(tokenType, rand.Reader)
}

func generateToken(tokenType TokenType, random io.Reader) (Token, error) {
	if !tokenType.valid() {
		return Token{}, fmt.Errorf("%w: unsupported type", ErrInvalidToken)
	}
	if random == nil {
		return Token{}, errors.New("token random source is required")
	}
	var kid [kidBytes]byte
	if _, err := io.ReadFull(random, kid[:]); err != nil {
		return Token{}, fmt.Errorf("generate token kid: %w", err)
	}
	var secret [secretBytes]byte
	if _, err := io.ReadFull(random, secret[:]); err != nil {
		return Token{}, fmt.Errorf("generate token secret: %w", err)
	}
	return Token{tokenType: tokenType, kid: hex.EncodeToString(kid[:]), secret: secret}, nil
}

// ParseToken strictly parses one canonical V1 plaintext token.
func ParseToken(plaintext string) (Token, error) {
	if !strings.HasPrefix(plaintext, tokenPrefix) {
		return Token{}, ErrInvalidToken
	}
	remainder := strings.TrimPrefix(plaintext, tokenPrefix)
	typeEnd := strings.IndexByte(remainder, '_')
	if typeEnd <= 0 {
		return Token{}, ErrInvalidToken
	}
	tokenType := TokenType(remainder[:typeEnd])
	if !tokenType.valid() {
		return Token{}, ErrInvalidToken
	}
	remainder = remainder[typeEnd+1:]
	versionPrefix := tokenVersion + "_"
	if !strings.HasPrefix(remainder, versionPrefix) {
		return Token{}, ErrInvalidToken
	}
	remainder = strings.TrimPrefix(remainder, versionPrefix)
	if len(remainder) != kidEncodedBytes+1+secretEncodedBytes || remainder[kidEncodedBytes] != '_' {
		return Token{}, ErrInvalidToken
	}
	kid := remainder[:kidEncodedBytes]
	if !isLowerHex(kid) {
		return Token{}, ErrInvalidToken
	}
	encodedSecret := remainder[kidEncodedBytes+1:]
	if !isBase64URL(encodedSecret) {
		return Token{}, ErrInvalidToken
	}
	decodedSecret, err := base64.RawURLEncoding.Strict().DecodeString(encodedSecret)
	if err != nil || len(decodedSecret) != secretBytes {
		return Token{}, ErrInvalidToken
	}
	var secret [secretBytes]byte
	copy(secret[:], decodedSecret)
	return Token{tokenType: tokenType, kid: kid, secret: secret}, nil
}

// Type returns the token protocol type.
func (t Token) Type() TokenType {
	return t.tokenType
}

// KID returns the non-secret lookup identifier.
func (t Token) KID() string {
	return t.kid
}

// Encode explicitly reveals canonical plaintext for a one-time issuance response.
func (t Token) Encode() (string, error) {
	if !t.tokenType.valid() || len(t.kid) != kidEncodedBytes || !isLowerHex(t.kid) {
		return "", ErrInvalidToken
	}
	encodedSecret := base64.RawURLEncoding.EncodeToString(t.secret[:])
	if len(encodedSecret) != secretEncodedBytes {
		return "", ErrInvalidToken
	}
	return tokenPrefix + string(t.tokenType) + "_" + tokenVersion + "_" + t.kid + "_" + encodedSecret, nil
}

// Digest returns the domain-separated value persisted for verification.
func (t Token) Digest() (Digest, error) {
	if !t.tokenType.valid() || len(t.kid) != kidEncodedBytes || !isLowerHex(t.kid) {
		return Digest{}, ErrInvalidToken
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(digestDomain))
	_, _ = hash.Write([]byte(t.tokenType))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(t.kid))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(t.secret[:])
	var digest Digest
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

// EqualDigest compares two digests without data-dependent early exit.
func EqualDigest(first, second Digest) bool {
	return subtle.ConstantTimeCompare(first[:], second[:]) == 1
}

func (t TokenType) valid() bool {
	switch t {
	case TokenPersonalAccess, TokenCapability, TokenSession, TokenWebhookSecret:
		return true
	default:
		return false
	}
}

func isLowerHex(value string) bool {
	if len(value) != kidEncodedBytes {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func isBase64URL(value string) bool {
	if len(value) != secretEncodedBytes {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') &&
			(character < 'a' || character > 'z') &&
			(character < '0' || character > '9') &&
			character != '-' && character != '_' {
			return false
		}
	}
	return true
}
