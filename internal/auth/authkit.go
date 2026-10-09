// Package authkit verifies signed identities for local sessions and optional external login.
package authkit

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var ErrInvalidToken = errors.New("invalid or expired identity token")

type Config struct {
	Secret        []byte
	Issuers       []string
	Audience      string
	LeewaySeconds int
}

type Identity struct {
	UserID string
	Email  string
	Claims jwt.MapClaims
}

type Verifier struct {
	secret  []byte
	issuers map[string]struct{}
	parser  *jwt.Parser
}

func New(cfg Config) (*Verifier, error) {
	if len(cfg.Secret) < 32 {
		return nil, fmt.Errorf("JWT signing secret must contain at least 32 bytes")
	}
	if cfg.LeewaySeconds < 0 || cfg.LeewaySeconds > 60 {
		return nil, fmt.Errorf("JWT leeway must be between 0 and 60 seconds")
	}
	if cfg.Audience == "" {
		return nil, fmt.Errorf("JWT audience is required")
	}
	issuers := make(map[string]struct{})
	for _, iss := range cfg.Issuers {
		if iss != "" {
			issuers[iss] = struct{}{}
		}
	}
	if len(issuers) == 0 {
		return nil, fmt.Errorf("JWT issuer allowlist is required")
	}
	return &Verifier{secret: append([]byte(nil), cfg.Secret...), issuers: issuers, parser: jwt.NewParser(jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(), jwt.WithAudience(cfg.Audience), jwt.WithIssuedAt(), jwt.WithLeeway(time.Duration(cfg.LeewaySeconds)*time.Second))}, nil
}

func (v *Verifier) Verify(raw string) (*Identity, error) {
	claims := jwt.MapClaims{}
	token, err := v.parser.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) { return v.secret, nil })
	if err != nil || token == nil || !token.Valid {
		return nil, ErrInvalidToken
	}
	iss, err := claims.GetIssuer()
	if err != nil {
		return nil, ErrInvalidToken
	}
	if _, ok := v.issuers[iss]; !ok {
		return nil, ErrInvalidToken
	}
	sub, _ := claims["sub"].(string)
	email, _ := claims["email"].(string)
	if sub == "" || email == "" {
		return nil, ErrInvalidToken
	}
	return &Identity{UserID: sub, Email: email, Claims: claims}, nil
}
