// Package server implements the optional generic OIDC SSO login adapter.
package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"
	authkit "github.com/x0c/shell-gate/internal/auth"
	"golang.org/x/oauth2"
)

const (
	envSSOIssuer          = "SHELL_SSO_ISSUER"
	envSSOClientID        = "SHELL_SSO_CLIENT_ID"
	envSSOClientSecret    = "SHELL_SSO_CLIENT_SECRET"
	envSSOScopes          = "SHELL_SSO_SCOPES"
	envSSOEmailClaim      = "SHELL_SSO_EMAIL_CLAIM"
	envSSOAllowUnverified = "SHELL_SSO_ALLOW_UNVERIFIED_EMAIL"
)

const (
	ssoStateTTL      = 10 * time.Minute
	ssoDiscoveryTTL  = time.Hour
	ssoHTTPTimeout   = 10 * time.Second
	ssoStateIssuer   = "shellgate-sso-state"
	ssoStateAudience = "shellgate"
)

// SSOConfig describes one generic OIDC issuer. All fields come from the
// process environment; nothing is read from neighboring checkouts.
type SSOConfig struct {
	Issuer          string
	ClientID        string
	ClientSecret    string
	Scopes          []string
	EmailClaim      string
	AllowUnverified bool
}

// parseSSOConfig reads the optional generic SSO policy. Syntax errors fail
// fast; IdP reachability is checked lazily at login time so a short IdP
// outage never blocks gateway startup.
func parseSSOConfig() (SSOConfig, error) {
	issuer := strings.TrimSpace(os.Getenv(envSSOIssuer))
	clientID := strings.TrimSpace(os.Getenv(envSSOClientID))
	secret := os.Getenv(envSSOClientSecret)
	if issuer == "" || clientID == "" || secret == "" {
		return SSOConfig{}, fmt.Errorf("%s, %s and %s must all be set for SSO", envSSOIssuer, envSSOClientID, envSSOClientSecret)
	}
	scopes := parseSSOScopes(os.Getenv(envSSOScopes))
	emailClaim := strings.TrimSpace(os.Getenv(envSSOEmailClaim))
	if emailClaim == "" {
		emailClaim = "email"
	}
	if !validClaimName(emailClaim) {
		return SSOConfig{}, fmt.Errorf("%s must be 1-64 chars of letters/digits/._-", envSSOEmailClaim)
	}
	allowUnverified, err := parseBool(envSSOAllowUnverified, false)
	if err != nil {
		return SSOConfig{}, err
	}
	cfg := SSOConfig{
		Issuer:          issuer,
		ClientID:        clientID,
		ClientSecret:    secret,
		Scopes:          scopes,
		EmailClaim:      emailClaim,
		AllowUnverified: allowUnverified,
	}
	return cfg, cfg.validate()
}

func (c SSOConfig) validate() error {
	u, err := url.ParseRequestURI(c.Issuer)
	if err != nil || !validSSOEndpoint(c.Issuer, true) || u.RawQuery != "" || u.ForceQuery || strings.Contains(c.Issuer, "#") {
		return errors.New("invalid SSO issuer URL")
	}
	if strings.TrimSpace(c.ClientID) == "" || len(c.ClientID) > 256 || len(c.ClientSecret) < 8 || strings.TrimSpace(c.ClientSecret) == "" {
		return errors.New("incomplete SSO client configuration")
	}
	if !validClaimName(c.EmailClaim) {
		return errors.New("invalid SSO email claim")
	}
	for _, scope := range c.Scopes {
		for _, r := range scope {
			if r < 0x21 || r > 0x7e || r == '"' || r == '\\' {
				return errors.New("invalid SSO scope")
			}
		}
	}
	return nil
}

func parseSSOScopes(raw string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, 4)
	for _, part := range strings.Fields(strings.ReplaceAll(raw, ",", " ")) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, dup := seen[part]; dup {
			continue
		}
		seen[part] = struct{}{}
		out = append(out, part)
	}
	hasOpenID := false
	for _, s := range out {
		if s == "openid" {
			hasOpenID = true
		}
	}
	if !hasOpenID {
		out = append([]string{"openid"}, out...)
	}
	if strings.TrimSpace(raw) == "" {
		out = append(out, "email", "profile")
	}
	return out
}

func validClaimName(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

func isLocalHostname(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

// Limit every provider response, including discovery, JWKS and token errors.
// A provider may host endpoints on different HTTPS origins, but never downgrade
// a remote issuer to HTTP or forward credentials through a redirect.
type oidcTransport struct {
	base      *http.Transport
	localHTTP bool
}

func validSSOEndpoint(raw string, localHTTP bool) bool {
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	return u.Scheme == "https" || (localHTTP && u.Scheme == "http" && isLocalHostname(u.Hostname()))
}

func (t *oidcTransport) CloseIdleConnections() { t.base.CloseIdleConnections() }

func (t *oidcTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !validSSOEndpoint(r.URL.String(), t.localHTTP) {
		return nil, errors.New("unsafe OIDC endpoint")
	}
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	closeErr := resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(raw) > 1<<20 {
		return nil, errors.New("OIDC response too large")
	}
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	return resp, nil
}

type oidcClient struct {
	cfg       SSOConfig
	http      *http.Client
	mu        sync.Mutex
	provider  *oidc.Provider
	verifier  *oidc.IDTokenVerifier
	discExp   time.Time
	authStyle oauth2.AuthStyle
}

func newOIDCClient(cfg SSOConfig) *oidcClient {
	return &oidcClient{cfg: cfg, http: &http.Client{
		Timeout:       ssoHTTPTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &oidcTransport{
			localHTTP: strings.HasPrefix(cfg.Issuer, "http://"),
			base:      &http.Transport{Proxy: nil, MaxIdleConns: 8, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: ssoHTTPTimeout},
		},
	}}
}

func (c *oidcClient) discovery(ctx context.Context) (*oidc.Provider, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.provider != nil && time.Now().Before(c.discExp) {
		return c.provider, nil
	}
	p, err := oidc.NewProvider(oidc.ClientContext(ctx, c.http), c.cfg.Issuer)
	if err != nil {
		return nil, err
	}
	var metadata struct {
		Issuer      string   `json:"issuer"`
		JWKSURI     string   `json:"jwks_uri"`
		AuthMethods []string `json:"token_endpoint_auth_methods_supported"`
		SigningAlgs []string `json:"id_token_signing_alg_values_supported"`
	}
	if err := p.Claims(&metadata); err != nil {
		return nil, err
	}
	if metadata.Issuer != c.cfg.Issuer {
		return nil, errors.New("OIDC issuer mismatch")
	}
	for _, endpoint := range []string{p.Endpoint().AuthURL, p.Endpoint().TokenURL, p.UserInfoEndpoint(), metadata.JWKSURI} {
		if endpoint != "" && !validSSOEndpoint(endpoint, strings.HasPrefix(c.cfg.Issuer, "http://")) {
			return nil, errors.New("unsafe OIDC endpoint")
		}
	}
	if p.Endpoint().AuthURL == "" || p.Endpoint().TokenURL == "" || metadata.JWKSURI == "" {
		return nil, errors.New("missing OIDC endpoint")
	}
	// Restrict algorithms to the intersection with the issuer's advertised set.
	algs := metadata.SigningAlgs
	if len(algs) == 0 {
		algs = []string{oidc.RS256}
	}
	supported := make([]string, 0, len(algs))
	for _, alg := range algs {
		switch alg {
		case "RS256", "RS384", "RS512", "ES256", "ES384", "ES512", "PS256", "PS384", "PS512", "EdDSA":
			supported = append(supported, alg)
		}
	}
	if len(supported) == 0 {
		return nil, errors.New("provider has no asymmetric signing algorithm")
	}
	style := oauth2.AuthStyleInHeader // OIDC default when the field is omitted.
	if len(metadata.AuthMethods) > 0 {
		style = oauth2.AuthStyleAutoDetect
		for _, m := range metadata.AuthMethods {
			if m == "client_secret_post" {
				style = oauth2.AuthStyleInParams
			}
		}
		for _, m := range metadata.AuthMethods {
			if m == "client_secret_basic" {
				style = oauth2.AuthStyleInHeader
			}
		}
		if style == oauth2.AuthStyleAutoDetect {
			return nil, errors.New("provider requires unsupported client authentication")
		}
	}
	c.provider = p
	c.verifier = p.Verifier(&oidc.Config{ClientID: c.cfg.ClientID, SupportedSigningAlgs: supported})
	c.authStyle = style
	c.discExp = time.Now().Add(ssoDiscoveryTTL)
	return p, nil
}

func (c *oidcClient) oauthConfig(p *oidc.Provider, redirect string) *oauth2.Config {
	c.mu.Lock()
	style := c.authStyle
	c.mu.Unlock()
	ep := p.Endpoint()
	ep.AuthStyle = style
	return &oauth2.Config{ClientID: c.cfg.ClientID, ClientSecret: c.cfg.ClientSecret, Endpoint: ep, RedirectURL: redirect, Scopes: c.cfg.Scopes}
}

func (c *oidcClient) verifyIDToken(ctx context.Context, raw, nonce string) (*oidc.IDToken, error) {
	if _, err := c.discovery(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	verifier := c.verifier
	c.mu.Unlock()
	token, err := verifier.Verify(oidc.ClientContext(ctx, c.http), raw)
	if err != nil {
		return nil, err
	}
	if token.Issuer != c.cfg.Issuer || token.Subject == "" || token.IssuedAt.IsZero() || token.IssuedAt.After(time.Now().Add(time.Minute)) {
		return nil, errors.New("invalid identity claims")
	}
	if nonce == "" || subtle.ConstantTimeCompare([]byte(token.Nonce), []byte(nonce)) != 1 {
		return nil, errors.New("invalid nonce")
	}
	var claims map[string]any
	if err := token.Claims(&claims); err != nil {
		return nil, err
	}
	if azp, exists := claims["azp"]; exists {
		if azp != c.cfg.ClientID {
			return nil, errors.New("invalid authorized party")
		}
	} else if len(token.Audience) > 1 {
		return nil, errors.New("missing authorized party")
	}
	return token, nil
}

var errSSOUnverified = errors.New("email address is not verified")
var errSSONoEmail = errors.New("identity has no email address")

func (c *oidcClient) emailFromClaims(claims map[string]any) (string, error) {
	value, _ := claims[c.cfg.EmailClaim].(string)
	email := normalizeEmail(value)
	if email == "" {
		return "", errSSONoEmail
	}
	verified, _ := claims["email_verified"].(bool)
	if !c.cfg.AllowUnverified && !verified {
		return "", errSSOUnverified
	}
	return email, nil
}

func normalizeEmail(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	addr, err := mail.ParseAddress(value)
	if err != nil || addr.Address != value || strings.ContainsAny(value, "\r\n") {
		return ""
	}
	return value
}

func (c *oidcClient) identity(ctx context.Context, p *oidc.Provider, token *oauth2.Token, nonce string) (*authkit.Identity, error) {
	raw, _ := token.Extra("id_token").(string)
	id, err := c.verifyIDToken(ctx, raw, nonce)
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	if err := id.Claims(&claims); err != nil {
		return nil, err
	}
	email, err := c.emailFromClaims(claims)
	if errors.Is(err, errSSONoEmail) && p.UserInfoEndpoint() != "" && token.AccessToken != "" {
		info, infoErr := p.UserInfo(oidc.ClientContext(ctx, c.http), oauth2.StaticTokenSource(token))
		if infoErr != nil {
			return nil, infoErr
		}
		if info.Subject == "" || info.Subject != id.Subject {
			return nil, errors.New("userinfo subject mismatch")
		}
		claims = make(map[string]any)
		if err := info.Claims(&claims); err != nil {
			return nil, err
		}
		email, err = c.emailFromClaims(claims)
	}
	if err != nil {
		return nil, err
	}
	return &authkit.Identity{UserID: id.Subject, Email: email}, nil
}

func (s *Server) ssoStateCookieName() string { return s.cfg.CookieName + "_sso_state" }

// ssoRedirectURI is the single pre-registered callback. With edge routing the
// apex owns login, so the callback lives on the apex origin and the session
// cookie (CookieDomain) carries over to edges.
func (s *Server) ssoRedirectURI() string {
	if s.cfg.EdgeRoutingEnabled() {
		return s.cfg.ApexOrigin() + "/api/sso/callback"
	}
	return strings.TrimRight(s.cfg.PublicURL, "/") + "/api/sso/callback"
}

type ssoStateClaims struct {
	jwt.RegisteredClaims
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
}

func (s *Server) mintSSOState(w http.ResponseWriter, state, nonce, verifier string) error {
	now := time.Now()
	claims := ssoStateClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    ssoStateIssuer,
			Audience:  jwt.ClaimStrings{ssoStateAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ssoStateTTL)),
		},
		State:    state,
		Nonce:    nonce,
		Verifier: verifier,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	raw, err := tok.SignedString(s.sessionSecret)
	if err != nil {
		return err
	}
	c := &http.Cookie{
		Name:     s.ssoStateCookieName(),
		Value:    raw,
		Path:     "/api/sso/callback",
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ssoStateTTL.Seconds()),
	}
	http.SetCookie(w, c)
	return nil
}

func (s *Server) consumeSSOState(r *http.Request) (state, nonce, verifier string, err error) {
	c, err := r.Cookie(s.ssoStateCookieName())
	if err != nil || c.Value == "" {
		return "", "", "", fmt.Errorf("missing login state")
	}
	claims := ssoStateClaims{}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithExpirationRequired(),
		jwt.WithIssuer(ssoStateIssuer),
		jwt.WithAudience(ssoStateAudience),
	)
	tok, err := parser.ParseWithClaims(c.Value, &claims, func(t *jwt.Token) (any, error) {
		return s.sessionSecret, nil
	})
	if err != nil || tok == nil || !tok.Valid {
		return "", "", "", fmt.Errorf("invalid login state")
	}
	if claims.State == "" || claims.Nonce == "" || claims.Verifier == "" {
		return "", "", "", fmt.Errorf("invalid login state")
	}
	return claims.State, claims.Nonce, claims.Verifier, nil
}

func (s *Server) clearSSOState(w http.ResponseWriter) {
	c := &http.Cookie{Name: s.ssoStateCookieName(), Value: "", Path: "/api/sso/callback", HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1}
	http.SetCookie(w, c)
}

// Only static reason/stage labels are logged: provider errors can contain
// authorization codes, access tokens, client credentials or response bodies.
func (s *Server) ssoFail(w http.ResponseWriter, r *http.Request, code, stage string) {
	log.Printf("SSO sign-in failed: reason=%s stage=%s", code, stage)
	base := "/login"
	if s.cfg.EdgeRoutingEnabled() {
		base = s.cfg.ApexOrigin() + base
	}
	http.Redirect(w, r, base+"?error="+url.QueryEscape(code), http.StatusFound)
}

func (s *Server) handleSSOLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AuthMode != "sso" || s.oidc == nil {
		http.NotFound(w, r)
		return
	}
	if s.cfg.EdgeRoutingEnabled() && !s.cfg.IsApexHost(r.Host) {
		http.Redirect(w, r, s.cfg.ApexOrigin()+"/api/sso/login", http.StatusFound)
		return
	}
	if _, ok := s.identityFromCookie(r); ok {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	p, err := s.oidc.discovery(r.Context())
	if err != nil {
		s.ssoFail(w, r, "provider_unavailable", "discovery")
		return
	}
	state, nonce, verifier := rand.Text(), rand.Text(), oauth2.GenerateVerifier()
	if err := s.mintSSOState(w, state, nonce, verifier); err != nil {
		s.ssoFail(w, r, "login_failed", "state")
		return
	}
	config := s.oidc.oauthConfig(p, s.ssoRedirectURI())
	http.Redirect(w, r, config.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

func (s *Server) handleSSOCallback(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AuthMode != "sso" || s.oidc == nil {
		http.NotFound(w, r)
		return
	}
	if s.cfg.EdgeRoutingEnabled() && !s.cfg.IsApexHost(r.Host) {
		http.NotFound(w, r)
		return
	}
	s.clearSSOState(w)
	want, nonce, verifier, err := s.consumeSSOState(r)
	if err != nil {
		s.ssoFail(w, r, "expired", "state")
		return
	}
	q := r.URL.Query()
	if len(q["state"]) != 1 || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(want)) != 1 {
		s.ssoFail(w, r, "login_failed", "state")
		return
	}
	if q.Get("error") != "" {
		s.ssoFail(w, r, "access_denied", "provider_denied")
		return
	}
	if len(q["code"]) != 1 || q.Get("code") == "" {
		s.ssoFail(w, r, "login_failed", "code")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	p, err := s.oidc.discovery(ctx)
	if err != nil {
		s.ssoFail(w, r, "provider_unavailable", "discovery")
		return
	}
	token, err := s.oidc.oauthConfig(p, s.ssoRedirectURI()).Exchange(oidc.ClientContext(ctx, s.oidc.http), q.Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		s.ssoFail(w, r, "provider_unavailable", "exchange")
		return
	}
	id, err := s.oidc.identity(ctx, p, token, nonce)
	if err != nil {
		code := "login_failed"
		if errors.Is(err, errSSOUnverified) {
			code = "unverified_email"
		}
		s.ssoFail(w, r, code, "identity")
		return
	}
	if !s.cfg.emailAllowed(id.Email) {
		s.ssoFail(w, r, "not_allowed", "allowlist")
		return
	}
	if err := s.store.EnsureDefaults(id.Email); err != nil {
		s.ssoFail(w, r, "login_failed", "workspace")
		return
	}
	session, err := s.mintSessionToken(id)
	if err != nil {
		s.ssoFail(w, r, "login_failed", "session")
		return
	}
	http.SetCookie(w, s.sessionCookie(session, int(s.cfg.SessionTTL.Seconds())))
	http.Redirect(w, r, "/", http.StatusFound)
}
