package authkit

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"golang.org/x/crypto/argon2"
	"strings"
	"unicode/utf8"
)

// OWASP's Argon2id minimum: 19 MiB, two iterations, one lane.
const passwordPrefix = "$argon2id$v=19$m=19456,t=2,p=1$"

func ValidPassword(password string) bool {
	n := utf8.RuneCountInString(password)
	return utf8.ValidString(password) && n >= 12 && n <= 128
}

func HashPassword(password string) (string, error) {
	if !ValidPassword(password) {
		return "", fmt.Errorf("password must contain 12 to 128 characters")
	}
	salt := []byte(rand.Text())
	key := argon2.IDKey([]byte(password), salt, 2, 19*1024, 1, 32)
	return passwordPrefix + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key), nil
}

func CheckPassword(hash, password string) bool {
	// Only our bounded, versioned format is accepted. Stored data cannot request
	// unbounded Argon2 parameters or allocate arbitrary amounts of memory.
	if !strings.HasPrefix(hash, passwordPrefix) || !ValidPassword(password) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(hash, passwordPrefix), "$")
	if len(parts) != 2 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[0])
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil || len(want) != 32 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, 2, 19*1024, 1, 32)
	return subtle.ConstantTimeCompare(want, got) == 1
}
