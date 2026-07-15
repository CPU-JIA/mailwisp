// Package duckmail implements the isolated DuckMail compatibility adapter.
package duckmail

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	passwordMemoryKiB   = 19 * 1024
	passwordIterations  = 2
	passwordParallelism = 1
	passwordSaltBytes   = 16
	passwordKeyBytes    = 32
)

func hashPassword(password string) (string, error) {
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate DuckMail password salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, passwordIterations, passwordMemoryKiB, passwordParallelism, passwordKeyBytes)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, passwordMemoryKiB, passwordIterations, passwordParallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	parameters := strings.Split(parts[3], ",")
	if len(parameters) != 3 {
		return false
	}
	memory, ok := parseParameter(parameters[0], "m=")
	if !ok || memory != passwordMemoryKiB {
		return false
	}
	iterations, ok := parseParameter(parameters[1], "t=")
	if !ok || iterations != passwordIterations {
		return false
	}
	parallelism, ok := parseParameter(parameters[2], "p=")
	if !ok || parallelism != passwordParallelism {
		return false
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) != passwordSaltBytes {
		return false
	}
	want, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(want) != passwordKeyBytes {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, uint32(iterations), uint32(memory), uint8(parallelism), passwordKeyBytes)
	return subtle.ConstantTimeCompare(got, want) == 1
}

func parseParameter(raw, prefix string) (int, bool) {
	if !strings.HasPrefix(raw, prefix) {
		return 0, false
	}
	value, err := strconv.Atoi(strings.TrimPrefix(raw, prefix))
	return value, err == nil && value > 0
}
