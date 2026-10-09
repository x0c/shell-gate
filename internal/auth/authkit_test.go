package authkit

import (
	"github.com/golang-jwt/jwt/v5"
	"testing"
	"time"
)

func TestVerifierRejectsInvalidIdentities(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	v, err := New(Config{Secret: key, Issuers: []string{"trusted"}, Audience: "shellgate"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, field string
		value       any
		method      jwt.SigningMethod
	}{
		{"valid", "", nil, jwt.SigningMethodHS256},
		{"expired", "exp", time.Now().Add(-time.Hour).Unix(), jwt.SigningMethodHS256},
		{"missing expiry", "exp", nil, jwt.SigningMethodHS256},
		{"wrong issuer", "iss", "untrusted", jwt.SigningMethodHS256},
		{"wrong audience", "aud", "other", jwt.SigningMethodHS256},
		{"missing subject", "sub", "", jwt.SigningMethodHS256},
		{"future issued", "iat", time.Now().Add(time.Hour).Unix(), jwt.SigningMethodHS256},
		{"wrong algorithm", "", nil, jwt.SigningMethodHS384},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := jwt.MapClaims{"sub": "user-id", "email": "user@example.com", "iss": "trusted", "aud": "shellgate", "exp": time.Now().Add(time.Hour).Unix()}
			if tc.field != "" {
				if tc.value == nil {
					delete(c, tc.field)
				} else {
					c[tc.field] = tc.value
				}
			}
			raw, err := jwt.NewWithClaims(tc.method, c).SignedString(key)
			if err != nil {
				t.Fatal(err)
			}
			_, err = v.Verify(raw)
			if (err == nil) != (tc.name == "valid") {
				t.Fatalf("verification: %v", err)
			}
		})
	}
}
