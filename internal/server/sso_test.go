package server

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/x0c/shell-gate/internal/server/store"
)

type mockIdP struct {
	t           *testing.T
	server      *httptest.Server
	priv        *rsa.PrivateKey
	kid         string
	idToken     string
	userinfo    map[string]any
	tokenHit    atomic.Int32
	userinfoHit atomic.Int32
	metadata    map[string]any
	override    func(http.ResponseWriter, *http.Request) bool
}

func newMockIdP(t *testing.T) *mockIdP {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	m := &mockIdP{t: t, priv: priv, kid: "test-key", userinfo: map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		doc := map[string]any{
			"issuer":                 base,
			"authorization_endpoint": base + "/auth",
			"token_endpoint":         base + "/token",
			"userinfo_endpoint":      base + "/userinfo",
			"jwks_uri":               base + "/jwks",
		}
		for k, v := range m.metadata {
			doc[k] = v
		}
		writeJSON(w, http.StatusOK, doc)
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(m.priv.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(m.priv.E)).Bytes())
		writeJSON(w, http.StatusOK, map[string]any{
			"keys": []map[string]string{{"kid": m.kid, "kty": "RSA", "n": n, "e": e}},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		m.tokenHit.Add(1)
		writeJSON(w, http.StatusOK, map[string]string{"id_token": m.idToken, "access_token": "mock-access", "token_type": "Bearer"})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		m.userinfoHit.Add(1)
		writeJSON(w, http.StatusOK, m.userinfo)
	})
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.override != nil && m.override(w, r) {
			return
		}
		mux.ServeHTTP(w, r)
	}))
	return m
}

func (m *mockIdP) issuer() string { return m.server.URL }

func (m *mockIdP) sign(email, nonce string, extra map[string]any) string {
	claims := jwt.MapClaims{
		"iss":            m.issuer(),
		"aud":            "test-client",
		"sub":            "user-1",
		"email":          email,
		"email_verified": true,
		"nonce":          nonce,
		"exp":            time.Now().Add(time.Hour).Unix(),
		"iat":            time.Now().Unix(),
	}
	for k, v := range extra {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = m.kid
	raw, err := tok.SignedString(m.priv)
	if err != nil {
		m.t.Fatal(err)
	}
	return raw
}

func newSSOTestServer(t *testing.T, idp *mockIdP, allow []string) *Server {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	emails := map[string]struct{}{}
	for _, e := range allow {
		emails[strings.ToLower(e)] = struct{}{}
	}
	cfg := Config{
		AuthMode:      "sso",
		PublicURL:     "http://localhost:18765",
		Listen:        "127.0.0.1:0",
		AllowedEmails: emails,
		Machines:      []MachineConfig{{Name: "default", URL: mustURL(t, "http://127.0.0.1:65535")}},
		CookieName:    "shell_session",
		CookieSecure:  false,
		SessionTTL:    time.Hour,
		DataDir:       dir,
		DatabaseURL:   filepath.Join(dir, "test.db"),
		SSO: SSOConfig{
			Issuer:       idp.issuer(),
			ClientID:     "test-client",
			ClientSecret: "test-secret-12345",
			Scopes:       []string{"openid", "email", "profile"},
			EmailClaim:   "email",
		},
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New sso: %v", err)
	}
	t.Cleanup(func() { _ = s.Shutdown() })
	// Point the OIDC client at the mock without extra env plumbing.
	s.oidc = newOIDCClient(cfg.SSO)
	return s
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestParseSSOConfigValidation(t *testing.T) {
	t.Setenv("SHELL_SSO_ISSUER", "https://sso.example.com/realms/x")
	t.Setenv("SHELL_SSO_CLIENT_ID", "gate")
	t.Setenv("SHELL_SSO_CLIENT_SECRET", "secret-12345")
	t.Setenv("SHELL_SSO_SCOPES", "")
	t.Setenv("SHELL_SSO_EMAIL_CLAIM", "")
	t.Setenv("SHELL_SSO_ALLOW_UNVERIFIED_EMAIL", "")
	cfg, err := parseSSOConfig()
	if err != nil {
		t.Fatalf("应解析成功: %v", err)
	}
	if len(cfg.Scopes) == 0 || cfg.Scopes[0] != "openid" {
		t.Fatalf("scopes 应默认含 openid: %+v", cfg)
	}
	t.Setenv("SHELL_SSO_ISSUER", "http://sso.example.com/realms/x")
	if _, err := parseSSOConfig(); err == nil {
		t.Fatal("公网 issuer 用 http 应失败")
	}
	t.Setenv("SHELL_SSO_ISSUER", "https://sso.example.com/realms/x")
	t.Setenv("SHELL_SSO_CLIENT_SECRET", "short")
	if _, err := parseSSOConfig(); err == nil {
		t.Fatal("过短 secret 应失败")
	}
}

func TestSSOLoginRedirect(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.server.Close()
	s := newSSOTestServer(t, idp, []string{"operator@example.com"})
	req := httptest.NewRequest(http.MethodGet, "/api/sso/login", nil)
	req.Host = "localhost:18765"
	w := httptest.NewRecorder()
	s.handleSSOLogin(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("应 302 到 IdP, 得到 %d %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("client_id") != "test-client" || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("auth 参数不对: %s", loc)
	}
	if q.Get("state") == "" || q.Get("nonce") == "" || q.Get("code_challenge") == "" {
		t.Fatalf("缺少 state/nonce/pkce: %s", loc)
	}
	if q.Get("redirect_uri") != "http://localhost:18765/api/sso/callback" {
		t.Fatalf("redirect_uri=%q", q.Get("redirect_uri"))
	}
	found := false
	for _, c := range w.Result().Cookies() {
		if c.Name == "shell_session_sso_state" && c.HttpOnly {
			found = true
		}
	}
	if !found {
		t.Fatal("应下发 sso_state Cookie")
	}
}

func TestSSOCallbackSuccess(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.server.Close()
	s := newSSOTestServer(t, idp, []string{"operator@example.com"})
	// 预签 state Cookie, nonce 由测试掌控, token 端按同一 nonce 签发。
	w0 := httptest.NewRecorder()
	if err := s.mintSSOState(w0, "state-1", "nonce-1", strings.Repeat("v", 43)); err != nil {
		t.Fatal(err)
	}
	var stateCookie *http.Cookie
	for _, c := range w0.Result().Cookies() {
		if c.Name == "shell_session_sso_state" {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("缺少 state cookie")
	}
	idp.idToken = idp.sign("operator@example.com", "nonce-1", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/sso/callback?code=authcode&state=state-1", nil)
	req.Host = "localhost:18765"
	req.AddCookie(stateCookie)
	w := httptest.NewRecorder()
	s.handleSSOCallback(w, req)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Fatalf("应 302 到 /, 得到 %d loc=%q body=%s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	sessionFound := false
	cleared := false
	for _, c := range w.Result().Cookies() {
		if c.Name == "shell_session" && c.Value != "" {
			sessionFound = true
		}
		if c.Name == "shell_session_sso_state" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !sessionFound {
		t.Fatal("应下发会话 Cookie")
	}
	if !cleared {
		t.Fatal("应用掉 state Cookie")
	}
}

func TestSSOCallbackRejectsBadState(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.server.Close()
	s := newSSOTestServer(t, idp, []string{"operator@example.com"})
	w0 := httptest.NewRecorder()
	if err := s.mintSSOState(w0, "state-1", "nonce-1", strings.Repeat("v", 43)); err != nil {
		t.Fatal(err)
	}
	var stateCookie *http.Cookie
	for _, c := range w0.Result().Cookies() {
		if c.Name == "shell_session_sso_state" {
			stateCookie = c
		}
	}
	idp.idToken = idp.sign("operator@example.com", "nonce-1", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/sso/callback?code=authcode&state=wrong", nil)
	req.Host = "localhost:18765"
	req.AddCookie(stateCookie)
	w := httptest.NewRecorder()
	s.handleSSOCallback(w, req)
	if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "/login?error=") {
		t.Fatalf("state 不符应回登录页, 得到 %d %q", w.Code, w.Header().Get("Location"))
	}
	if idp.tokenHit.Load() != 0 {
		t.Fatal("state 失败不应再调 token 端点")
	}
}

func TestSSOCallbackRejectsUnallowedEmail(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.server.Close()
	s := newSSOTestServer(t, idp, []string{"operator@example.com"})
	w0 := httptest.NewRecorder()
	if err := s.mintSSOState(w0, "s", "n", strings.Repeat("v", 43)); err != nil {
		t.Fatal(err)
	}
	var sc *http.Cookie
	for _, c := range w0.Result().Cookies() {
		if c.Name == "shell_session_sso_state" {
			sc = c
		}
	}
	idp.idToken = idp.sign("someone@else.com", "n", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/sso/callback?code=c&state=s", nil)
	req.Host = "localhost:18765"
	req.AddCookie(sc)
	w := httptest.NewRecorder()
	s.handleSSOCallback(w, req)
	if w.Code != http.StatusFound || !strings.Contains(w.Header().Get("Location"), "not_allowed") {
		t.Fatalf("非白名单应 not_allowed, 得到 %q", w.Header().Get("Location"))
	}
}

func TestSSOCallbackRejectsUnverifiedEmail(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.server.Close()
	s := newSSOTestServer(t, idp, []string{"operator@example.com"})
	w0 := httptest.NewRecorder()
	if err := s.mintSSOState(w0, "s", "n", strings.Repeat("v", 43)); err != nil {
		t.Fatal(err)
	}
	var sc *http.Cookie
	for _, c := range w0.Result().Cookies() {
		if c.Name == "shell_session_sso_state" {
			sc = c
		}
	}
	idp.idToken = idp.sign("operator@example.com", "n", map[string]any{"email_verified": false})
	req := httptest.NewRequest(http.MethodGet, "/api/sso/callback?code=c&state=s", nil)
	req.Host = "localhost:18765"
	req.AddCookie(sc)
	w := httptest.NewRecorder()
	s.handleSSOCallback(w, req)
	if w.Code != http.StatusFound || !strings.Contains(w.Header().Get("Location"), "unverified_email") {
		t.Fatalf("未验证邮箱应 unverified_email, 得到 %q", w.Header().Get("Location"))
	}
}

func TestSSOVerifyRejectsHMAC(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.server.Close()
	s := newSSOTestServer(t, idp, []string{"operator@example.com"})
	raw := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": idp.issuer(), "aud": "test-client", "sub": "u", "email": "operator@example.com",
		"nonce": "n", "exp": time.Now().Add(time.Hour).Unix(),
	})
	hs, err := raw.SignedString([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.oidc.verifyIDToken(t.Context(), hs, "n"); err == nil {
		t.Fatal("HMAC 签名的 ID token 必须拒绝")
	}
}

func TestSSOUserinfoFallback(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.server.Close()
	s := newSSOTestServer(t, idp, []string{"operator@example.com"})
	w0 := httptest.NewRecorder()
	if err := s.mintSSOState(w0, "s", "n", strings.Repeat("v", 43)); err != nil {
		t.Fatal(err)
	}
	var sc *http.Cookie
	for _, c := range w0.Result().Cookies() {
		if c.Name == "shell_session_sso_state" {
			sc = c
		}
	}
	// id_token 无邮箱, userinfo 给出邮箱。
	idp.idToken = idp.sign("", "n", map[string]any{"email": nil})
	idp.userinfo = map[string]any{"sub": "user-1", "email": "operator@example.com", "email_verified": true}
	req := httptest.NewRequest(http.MethodGet, "/api/sso/callback?code=c&state=s", nil)
	req.Host = "localhost:18765"
	req.AddCookie(sc)
	w := httptest.NewRecorder()
	s.handleSSOCallback(w, req)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Fatalf("userinfo 兜底应成功, 得到 %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestSessionExchangeDisabledInSSOMode(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.server.Close()
	s := newSSOTestServer(t, idp, []string{"operator@example.com"})
	body, _ := json.Marshal(sessionRequest{AccessToken: "x"})
	req := httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	s.handleSession(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("sso 下 /api/session 应 404, 得到 %d", w.Code)
	}
}

// Exercise the public HTTP routes and cookie jar, not just direct handlers.
// The provider checks the challenge/verifier, callback, nonce and client auth.
func TestSSOHTTPFlow(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.server.Close()
	s := newSSOTestServer(t, idp, []string{"operator@example.com"})
	var gateURL string
	if binary := os.Getenv("SHELL_TEST_GATEWAY_BINARY"); binary != "" {
		gateURL = startSSOBinary(t, binary, s.cfg)
	} else {
		gate := httptest.NewServer(s.httpSrv.Handler)
		defer gate.Close()
		gateURL = gate.URL
	}
	s.cfg.PublicURL = gateURL
	var authorization url.Values
	idp.override = func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/auth":
			authorization = r.URL.Query()
			if authorization.Get("redirect_uri") != gateURL+"/api/sso/callback" || authorization.Get("code_challenge_method") != "S256" {
				t.Error("invalid authorization request")
			}
			target := authorization.Get("redirect_uri") + "?code=one-use-code&state=" + url.QueryEscape(authorization.Get("state"))
			http.Redirect(w, r, target, http.StatusFound)
			return true
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			client, secret, ok := r.BasicAuth()
			challenge := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if !ok || client != s.cfg.SSO.ClientID || secret != s.cfg.SSO.ClientSecret ||
				r.Form.Get("redirect_uri") != authorization.Get("redirect_uri") || r.Form.Get("code") != "one-use-code" ||
				base64.RawURLEncoding.EncodeToString(challenge[:]) != authorization.Get("code_challenge") {
				t.Error("code exchange did not bind client, callback and PKCE")
			}
			token := idp.sign("operator@example.com", authorization.Get("nonce"), nil)
			writeJSON(w, http.StatusOK, map[string]string{"id_token": token, "access_token": "mock-access", "token_type": "Bearer"})
			return true
		}
		return false
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	resp, err := client.Get(gateURL + "/api/sso/login")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/" {
		t.Fatalf("login did not reach workspace: %s %s", resp.Status, resp.Request.URL)
	}
	resp, err = client.Get(gateURL + "/api/machines")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session not accepted: %s", resp.Status)
	}
	resp, err = client.Get(gateURL + "/logout")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	resp, err = client.Get(gateURL + "/api/machines")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("logout retained session: %s", resp.Status)
	}
}

func runSSOCallback(t *testing.T, s *Server) *httptest.ResponseRecorder {
	t.Helper()
	state := httptest.NewRecorder()
	if err := s.mintSSOState(state, "s", "n", strings.Repeat("v", 43)); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/sso/callback?code=c&state=s", nil)
	req.Host = "localhost:18765"
	req.AddCookie(state.Result().Cookies()[0])
	w := httptest.NewRecorder()
	s.handleSSOCallback(w, req)
	for _, c := range w.Result().Cookies() {
		if c.Name == s.ssoStateCookieName() && c.MaxAge < 0 {
			return w
		}
	}
	t.Fatal("callback did not clear state")
	return w
}

func TestSSOIdentityBoundaries(t *testing.T) {
	tests := []struct {
		name            string
		token           map[string]any
		info            map[string]any
		allowUnverified bool
		claim           string
		wantSuccess     bool
		wantUserinfo    bool
	}{
		{name: "missing verification", token: map[string]any{"email_verified": nil}},
		{name: "string verification", token: map[string]any{"email_verified": "true"}},
		{name: "explicit unverified policy", token: map[string]any{"email_verified": nil}, allowUnverified: true, wantSuccess: true},
		{name: "no subject", token: map[string]any{"sub": nil}},
		{name: "empty subject", token: map[string]any{"sub": ""}},
		{name: "wrong audience", token: map[string]any{"aud": "other"}},
		{name: "wrong issuer", token: map[string]any{"iss": "https://other.example.com"}},
		{name: "expired", token: map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}},
		{name: "no expiry", token: map[string]any{"exp": nil}},
		{name: "no issued at", token: map[string]any{"iat": nil}},
		{name: "future issued at", token: map[string]any{"iat": time.Now().Add(time.Hour).Unix()}},
		{name: "wrong nonce", token: map[string]any{"nonce": "wrong"}},
		{name: "wrong authorized party", token: map[string]any{"azp": "other"}},
		{name: "multiple audiences without authorized party", token: map[string]any{"aud": []string{"test-client", "other"}}},
		{name: "valid authorized party", token: map[string]any{"aud": []string{"test-client", "other"}, "azp": "test-client"}, wantSuccess: true},
		{name: "no username alias", token: map[string]any{"email": nil, "preferred_username": "operator@example.com"}, info: map[string]any{"sub": "user-1"}, wantUserinfo: true},
		{name: "explicit custom claim", claim: "mail", token: map[string]any{"email": nil, "mail": "operator@example.com"}, wantSuccess: true},
		{name: "display name is not an address", token: map[string]any{"email": "Operator <operator@example.com>"}, wantUserinfo: true},
		{name: "userinfo different subject", token: map[string]any{"email": nil}, info: map[string]any{"sub": "other", "email": "operator@example.com", "email_verified": true}, wantUserinfo: true},
		{name: "userinfo missing subject", token: map[string]any{"email": nil}, info: map[string]any{"email": "operator@example.com", "email_verified": true}, wantUserinfo: true},
		{name: "userinfo missing verification cannot inherit token flag", token: map[string]any{"email": nil}, info: map[string]any{"sub": "user-1", "email": "operator@example.com"}, wantUserinfo: true},
		{name: "userinfo false verification", token: map[string]any{"email": nil}, info: map[string]any{"sub": "user-1", "email": "operator@example.com", "email_verified": false}, wantUserinfo: true},
		{name: "userinfo verified", token: map[string]any{"email": nil}, info: map[string]any{"sub": "user-1", "email": "operator@example.com", "email_verified": true}, wantSuccess: true, wantUserinfo: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idp := newMockIdP(t)
			defer idp.server.Close()
			s := newSSOTestServer(t, idp, []string{"operator@example.com"})
			s.cfg.SSO.AllowUnverified = tt.allowUnverified
			if tt.claim != "" {
				s.cfg.SSO.EmailClaim = tt.claim
			}
			s.oidc = newOIDCClient(s.cfg.SSO)
			idp.idToken = idp.sign("operator@example.com", "n", tt.token)
			idp.userinfo = tt.info
			w := runSSOCallback(t, s)
			success := w.Header().Get("Location") == "/"
			if success != tt.wantSuccess {
				t.Fatalf("success=%v redirect=%s", success, w.Header().Get("Location"))
			}
			if (idp.userinfoHit.Load() > 0) != tt.wantUserinfo {
				t.Fatal("unexpected UserInfo fallback")
			}
			for _, c := range w.Result().Cookies() {
				if c.Name == s.cfg.CookieName && c.Value != "" && !tt.wantSuccess {
					t.Fatal("failure minted session")
				}
			}
		})
	}
}

func TestSSOTransportBoundaries(t *testing.T) {
	for _, mode := range []string{"redirect", "oversized", "userinfo credentials", "token downgrade", "no JWKS", "issuer mismatch", "unsupported client authentication"} {
		t.Run(mode, func(t *testing.T) {
			idp := newMockIdP(t)
			defer idp.server.Close()
			s := newSSOTestServer(t, idp, []string{"operator@example.com"})
			idp.metadata = map[string]any{}
			switch mode {
			case "redirect":
				idp.override = func(w http.ResponseWriter, r *http.Request) bool {
					http.Redirect(w, r, "/redirect-target", http.StatusTemporaryRedirect)
					return true
				}
			case "oversized":
				idp.override = func(w http.ResponseWriter, r *http.Request) bool {
					_, _ = w.Write([]byte(strings.Repeat(" ", (1<<20)+1)))
					return true
				}
			case "userinfo credentials":
				idp.metadata["userinfo_endpoint"] = "https://user:password@idp.example.com/userinfo"
			case "token downgrade":
				idp.metadata["token_endpoint"] = "http://idp.example.com/token"
			case "no JWKS":
				idp.metadata["jwks_uri"] = ""
			case "issuer mismatch":
				idp.metadata["issuer"] = idp.issuer() + "/"
			case "unsupported client authentication":
				idp.metadata["token_endpoint_auth_methods_supported"] = []string{"private_key_jwt"}
			}
			if _, err := s.oidc.discovery(t.Context()); err == nil {
				t.Fatal("unsafe discovery succeeded")
			}
		})
	}
}

func TestSSOSecretPostAndAuthorizationQuery(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.server.Close()
	s := newSSOTestServer(t, idp, []string{"operator@example.com"})
	idp.metadata = map[string]any{"token_endpoint_auth_methods_supported": []string{"client_secret_post"}, "authorization_endpoint": idp.issuer() + "/auth?tenant=example"}
	idp.override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/token" {
			return false
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("client_secret") != s.cfg.SSO.ClientSecret || r.Header.Get("Authorization") != "" {
			t.Error("incorrect client_secret_post")
		}
		writeJSON(w, http.StatusOK, map[string]string{"id_token": idp.sign("operator@example.com", "n", nil), "access_token": "mock-access", "token_type": "Bearer"})
		return true
	}
	w := runSSOCallback(t, s)
	if w.Header().Get("Location") != "/" {
		t.Fatal(w.Header().Get("Location"))
	}
	w = httptest.NewRecorder()
	s.handleSSOLogin(w, httptest.NewRequest(http.MethodGet, "/api/sso/login", nil))
	target, _ := url.Parse(w.Header().Get("Location"))
	if target.Query().Get("tenant") != "example" || target.Query().Get("state") == "" {
		t.Fatal("authorization endpoint query lost")
	}
}

func TestSSOStateFailures(t *testing.T) {
	for _, mode := range []string{"missing", "tampered", "expired", "wrong", "duplicate", "provider cancellation"} {
		t.Run(mode, func(t *testing.T) {
			idp := newMockIdP(t)
			defer idp.server.Close()
			s := newSSOTestServer(t, idp, []string{"operator@example.com"})
			state := httptest.NewRecorder()
			if err := s.mintSSOState(state, "s", "n", strings.Repeat("v", 43)); err != nil {
				t.Fatal(err)
			}
			cookie := state.Result().Cookies()[0]
			query := "?code=c&state=s"
			switch mode {
			case "tampered":
				cookie.Value += "x"
			case "expired":
				tok := jwt.NewWithClaims(jwt.SigningMethodHS256, ssoStateClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: ssoStateIssuer, Audience: jwt.ClaimStrings{ssoStateAudience}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Minute))}, State: "s", Nonce: "n", Verifier: strings.Repeat("v", 43)})
				var err error
				cookie.Value, err = tok.SignedString(s.sessionSecret)
				if err != nil {
					t.Fatal(err)
				}
			case "wrong":
				query = "?code=c&state=wrong"
			case "duplicate":
				query = "?code=c&state=s&state=wrong"
			case "provider cancellation":
				query = "?error=access_denied&state=s"
			}
			req := httptest.NewRequest(http.MethodGet, "/api/sso/callback"+query, nil)
			req.Host = "localhost:18765"
			if mode != "missing" {
				req.AddCookie(cookie)
			}
			w := httptest.NewRecorder()
			s.handleSSOCallback(w, req)
			if !strings.HasPrefix(w.Header().Get("Location"), "/login?error=") || idp.tokenHit.Load() != 0 {
				t.Fatalf("invalid state reached token exchange: %s", w.Header().Get("Location"))
			}
			if len(w.Result().Cookies()) != 1 || w.Result().Cookies()[0].MaxAge >= 0 {
				t.Fatal("failure retained login state")
			}
		})
	}
}

func TestSSOSigningKeyRotation(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.server.Close()
	s := newSSOTestServer(t, idp, []string{"operator@example.com"})
	first := idp.sign("operator@example.com", "n", nil)
	if _, err := s.oidc.verifyIDToken(t.Context(), first, "n"); err != nil {
		t.Fatal(err)
	}
	// Keep the same kid: a signature miss must refresh keys too.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp.priv = key
	second := idp.sign("operator@example.com", "n", nil)
	if _, err := s.oidc.verifyIDToken(t.Context(), second, "n"); err != nil {
		t.Fatalf("rotation failed: %v", err)
	}
	// A substituted signature must not trigger UserInfo recovery.
	idp.idToken = first
	idp.userinfo = map[string]any{"sub": "user-1", "email": "operator@example.com", "email_verified": true}
	w := runSSOCallback(t, s)
	if w.Header().Get("Location") == "/" || idp.userinfoHit.Load() != 0 {
		t.Fatal("invalid signature accepted or recovered through UserInfo")
	}
}

func TestSSOEdgeBoundaries(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.server.Close()
	s := newSSOTestServer(t, idp, []string{"operator@example.com"})
	s.cfg.Edges = []EdgeHost{{Name: "one", Host: "one.shell.example.com"}, {Name: "two", Host: "two.shell.example.com"}}
	s.cfg.CookieDomain = "shell.example.com"
	s.cfg.CookieSecure = true
	s.cfg.PublicURL = "https://shell.example.com"
	if s.ssoRedirectURI() != "https://shell.example.com/api/sso/callback" {
		t.Fatal("callback not on apex")
	}
	state := httptest.NewRecorder()
	if err := s.mintSSOState(state, "s", "n", strings.Repeat("v", 43)); err != nil {
		t.Fatal(err)
	}
	if cookie := state.Result().Cookies()[0]; cookie.Domain != "" || !cookie.Secure {
		t.Fatal("state must be apex-host-only and secure")
	}
	for _, host := range []string{"one.shell.example.com", "localhost:18765"} {
		req := httptest.NewRequest(http.MethodGet, "/api/sso/callback?code=c&state=s", nil)
		req.Host = host
		w := httptest.NewRecorder()
		s.httpSrv.Handler.ServeHTTP(w, req)
		if host == "localhost:18765" && w.Code != http.StatusMisdirectedRequest {
			t.Fatal("unexpected local host exemption")
		}
		if host != "localhost:18765" && w.Code != http.StatusNotFound {
			t.Fatal("callback accepted on edge")
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/test", nil)
	req.Host = "one.shell.example.com"
	req.Header.Set("Origin", "https://one.shell.example.com")
	called := false
	handler := s.protectRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Fatal("same-edge origin rejected")
	}
	called = false
	req.Header.Set("Origin", "https://two.shell.example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if called || w.Code != http.StatusForbidden {
		t.Fatal("cross-edge write accepted")
	}
}

func TestSSOSessionKeyBindsIdentityProvider(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.server.Close()
	first := newSSOTestServer(t, idp, []string{"operator@example.com"})
	second := newSSOTestServer(t, idp, []string{"operator@example.com"})
	if !bytes.Equal(first.sessionSecret, second.sessionSecret) {
		t.Fatal("same configuration cannot share sessions")
	}
	cfg := first.cfg
	cfg.SSO.ClientID = "different-client"
	other, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Shutdown() }()
	if bytes.Equal(first.sessionSecret, other.sessionSecret) {
		t.Fatal("client change did not isolate sessions")
	}
	cfg.SSO = first.cfg.SSO
	cfg.SSO.Issuer += "/other"
	other2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other2.Shutdown() }()
	if bytes.Equal(first.sessionSecret, other2.sessionSecret) {
		t.Fatal("issuer change did not isolate sessions")
	}
}

func TestSSOExplicitScopes(t *testing.T) {
	if got := strings.Join(parseSSOScopes("openid"), " "); got != "openid" {
		t.Fatalf("explicit scope expanded: %s", got)
	}
	if got := strings.Join(parseSSOScopes("email,email profile"), " "); got != "openid email profile" {
		t.Fatal(got)
	}
}

// An optional committed release binary exercises the same protocol contract.
func startSSOBinary(t *testing.T, binary string, cfg Config) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	origin := "http://" + address
	cmd := exec.Command(binary)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "SHELL_") && !strings.HasPrefix(value, "AUTH_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "SHELL_LISTEN="+address, "SHELL_PUBLIC_URL="+origin, "SHELL_DATA_DIR="+t.TempDir(),
		"SHELL_AUTH_MODE=sso", "SHELL_ALLOWED_EMAILS=operator@example.com", "SHELL_TTYD_URL="+cfg.Machines[0].URL.String(),
		"SHELL_NETMON_INTERVAL=0", "SHELL_SSO_ISSUER="+cfg.SSO.Issuer, "SHELL_SSO_CLIENT_ID="+cfg.SSO.ClientID, "SHELL_SSO_CLIENT_SECRET="+cfg.SSO.ClientSecret)
	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Signal(os.Interrupt); _ = cmd.Wait() })
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(origin + "/healthz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return origin
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("release binary did not become healthy")
	return ""
}
