package auth

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestGenerateAndParseTokenRoundTrip(t *testing.T) {
	t.Parallel()

	for _, tokenType := range []TokenType{TokenPersonalAccess, TokenCapability, TokenSession, TokenWebhookSecret} {
		t.Run(string(tokenType), func(t *testing.T) {
			t.Parallel()
			token, err := GenerateToken(tokenType)
			if err != nil {
				t.Fatalf("GenerateToken() error = %v", err)
			}
			plaintext, err := token.Encode()
			if err != nil {
				t.Fatalf("Encode() error = %v", err)
			}
			parsed, err := ParseToken(plaintext)
			if err != nil {
				t.Fatalf("ParseToken() error = %v", err)
			}
			if parsed.Type() != tokenType || parsed.KID() != token.KID() {
				t.Fatalf("parsed token = %q/%q, want %q/%q", parsed.Type(), parsed.KID(), token.Type(), token.KID())
			}
			encodedAgain, err := parsed.Encode()
			if err != nil || encodedAgain != plaintext {
				t.Fatalf("parsed Encode() = %q, %v", encodedAgain, err)
			}
			firstDigest, err := token.Digest()
			if err != nil {
				t.Fatalf("Digest(original) error = %v", err)
			}
			secondDigest, err := parsed.Digest()
			if err != nil || !EqualDigest(firstDigest, secondDigest) {
				t.Fatalf("Digest(parsed) mismatch, error = %v", err)
			}
		})
	}
}

func TestParseTokenRejectsNonCanonicalInput(t *testing.T) {
	t.Parallel()

	valid := deterministicToken(t, TokenCapability)
	plaintext, err := valid.Encode()
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	parts := tokenParts(t, plaintext)
	tests := map[string]string{
		"leading whitespace":  " " + plaintext,
		"trailing whitespace": plaintext + " ",
		"uppercase prefix":    "W" + plaintext[1:],
		"unknown type":        tokenPrefix + "unknown_" + tokenVersion + "_" + parts.kid + "_" + parts.secret,
		"unknown version":     tokenPrefix + string(TokenCapability) + "_v2_" + parts.kid + "_" + parts.secret,
		"uppercase kid":       tokenPrefix + string(TokenCapability) + "_" + tokenVersion + "_" + strings.ToUpper(parts.kid) + "_" + parts.secret,
		"short kid":           tokenPrefix + string(TokenCapability) + "_" + tokenVersion + "_" + parts.kid[:len(parts.kid)-1] + "_" + parts.secret,
		"short secret":        tokenPrefix + string(TokenCapability) + "_" + tokenVersion + "_" + parts.kid + "_" + parts.secret[:len(parts.secret)-1],
		"padded secret":       tokenPrefix + string(TokenCapability) + "_" + tokenVersion + "_" + parts.kid + "_" + parts.secret + "=",
		"invalid secret":      tokenPrefix + string(TokenCapability) + "_" + tokenVersion + "_" + parts.kid + "_" + parts.secret[:42] + "+",
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseToken(input); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("ParseToken() error = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestTokenDigestUsesDomainTypeKidAndRawSecret(t *testing.T) {
	t.Parallel()

	token := deterministicToken(t, TokenCapability)
	digest, err := token.Digest()
	if err != nil {
		t.Fatalf("Digest() error = %v", err)
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("mailwisp-token-v1"))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(TokenCapability))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(token.KID()))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(token.secret[:])
	var expected Digest
	copy(expected[:], hash.Sum(nil))
	if !EqualDigest(digest, expected) {
		t.Fatal("Digest() does not match the documented domain-separated formula")
	}

	other := token
	other.tokenType = TokenSession
	otherDigest, err := other.Digest()
	if err != nil {
		t.Fatalf("Digest(other type) error = %v", err)
	}
	if EqualDigest(digest, otherDigest) {
		t.Fatal("different token types produced equal digests")
	}
}

func TestGenerateTokenRejectsRandomFailure(t *testing.T) {
	t.Parallel()

	_, err := generateToken(TokenCapability, &tokenErrorReader{err: errors.New("entropy unavailable")})
	if err == nil || !strings.Contains(err.Error(), "entropy unavailable") {
		t.Fatalf("generateToken() error = %v", err)
	}
	partialRandom := io.MultiReader(
		bytes.NewReader(make([]byte, kidBytes)),
		&tokenErrorReader{err: errors.New("secret entropy unavailable")},
	)
	if _, err := generateToken(TokenCapability, partialRandom); err == nil || !strings.Contains(err.Error(), "secret entropy unavailable") {
		t.Fatalf("generateToken(secret failure) error = %v", err)
	}
	if _, err := generateToken("invalid", bytes.NewReader(make([]byte, kidBytes+secretBytes))); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("generateToken(invalid type) error = %v", err)
	}
}

func TestGenerateTokenProducesUniqueKIDsAndPlaintexts(t *testing.T) {
	const samples = 1_000
	kids := make(map[string]struct{}, samples)
	plaintexts := make(map[string]struct{}, samples)
	for range samples {
		token, err := GenerateToken(TokenCapability)
		if err != nil {
			t.Fatalf("GenerateToken() error = %v", err)
		}
		plaintext, err := token.Encode()
		if err != nil {
			t.Fatalf("Encode() error = %v", err)
		}
		if _, exists := kids[token.KID()]; exists {
			t.Fatalf("duplicate KID %q", token.KID())
		}
		if _, exists := plaintexts[plaintext]; exists {
			t.Fatal("duplicate token plaintext")
		}
		kids[token.KID()] = struct{}{}
		plaintexts[plaintext] = struct{}{}
	}
}

func FuzzParseTokenNeverPanics(f *testing.F) {
	valid := deterministicToken(f, TokenCapability)
	plaintext, err := valid.Encode()
	if err != nil {
		f.Fatalf("Encode() error = %v", err)
	}
	for _, seed := range []string{"", "wisp_", plaintext, strings.Repeat("x", 256)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		parsed, err := ParseToken(input)
		if err != nil {
			return
		}
		canonical, err := parsed.Encode()
		if err != nil {
			t.Fatalf("Encode(parsed) error = %v", err)
		}
		if canonical != input {
			t.Fatalf("successful ParseToken() accepted non-canonical input")
		}
	})
}

func BenchmarkParseToken(b *testing.B) {
	token := deterministicToken(b, TokenCapability)
	plaintext, err := token.Encode()
	if err != nil {
		b.Fatalf("Encode() error = %v", err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(plaintext)))
	for range b.N {
		if _, err := ParseToken(plaintext); err != nil {
			b.Fatalf("ParseToken() error = %v", err)
		}
	}
}

func BenchmarkTokenDigest(b *testing.B) {
	token := deterministicToken(b, TokenCapability)
	b.ReportAllocs()
	for range b.N {
		if _, err := token.Digest(); err != nil {
			b.Fatalf("Digest() error = %v", err)
		}
	}
}

func deterministicToken(t testing.TB, tokenType TokenType) Token {
	t.Helper()
	random := make([]byte, kidBytes+secretBytes)
	for index := range random {
		random[index] = byte(index + 1)
	}
	token, err := generateToken(tokenType, bytes.NewReader(random))
	if err != nil {
		t.Fatalf("generateToken() error = %v", err)
	}
	return token
}

type parsedTokenParts struct {
	kid    string
	secret string
}

func tokenParts(t testing.TB, plaintext string) parsedTokenParts {
	t.Helper()
	token, err := ParseToken(plaintext)
	if err != nil {
		t.Fatalf("ParseToken() error = %v", err)
	}
	prefix := fmt.Sprintf("%s%s_%s_", tokenPrefix, token.Type(), tokenVersion)
	remainder := strings.TrimPrefix(plaintext, prefix)
	return parsedTokenParts{kid: remainder[:kidEncodedBytes], secret: remainder[kidEncodedBytes+1:]}
}

type tokenErrorReader struct {
	err error
}

func (r *tokenErrorReader) Read([]byte) (int, error) {
	return 0, r.err
}

var _ io.Reader = (*tokenErrorReader)(nil)
