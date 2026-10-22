package server

import (
	"encoding/base32"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	authkit "github.com/x0c/shell-gate/internal/auth"
	"github.com/x0c/shell-gate/internal/server/store"
)

func totpRequest(s *Server, method, path string, body any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, "http://localhost:18769"+path, strings.NewReader(string(raw)))
	r.Header.Set("Origin", "http://localhost:18769")
	r.Header.Set("Content-Type", "application/json")
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.httpSrv.Handler.ServeHTTP(w, r)
	return w
}
func totpJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("request: %d %s", w.Code, w.Body.String())
	}
	var value map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}
func totpResponseCookie(t *testing.T, w *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == name && c.Value != "" {
			return c
		}
	}
	t.Fatalf("missing cookie %s: %d %s", name, w.Code, w.Body.String())
	return nil
}
func totpEnroll(t *testing.T, s *Server) (*http.Cookie, []string, string) {
	t.Helper()
	invite, err := s.store.Invite("user@example.com", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	reg := passwordPost(s, "/api/password/register", passwordRequest{Email: "user@example.com", Password: "a long test password", Token: invite})
	cookie := totpResponseCookie(t, reg, s.cfg.CookieName)
	setup := totpJSON(t, totpRequest(s, "POST", "/api/totp/setup", map[string]any{}, cookie))
	secret := setup["secret"].(string)
	state, err := s.store.TOTPState("user@example.com")
	if err != nil || state.Enabled() || strings.Contains(state.SetupSecret, secret) {
		t.Fatal("setup enabled or seed stored plaintext", err)
	}
	if !strings.HasPrefix(setup["qr"].(string), "data:image/png;base64,") {
		t.Fatal("missing local QR")
	}
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	enabled := totpRequest(s, "POST", "/api/totp/enable", map[string]any{"setupId": setup["setupId"], "code": code}, cookie)
	result := totpJSON(t, enabled)
	codes := []string{}
	for _, v := range result["recoveryCodes"].([]any) {
		codes = append(codes, v.(string))
	}
	if len(codes) != 10 {
		t.Fatal("missing recovery codes")
	}
	if old := totpRequest(s, "GET", "/api/totp", nil, cookie); old.Code != 401 {
		t.Fatal("old session remained valid", old.Code)
	}
	return totpResponseCookie(t, enabled, s.cfg.CookieName), codes, secret
}
func TestTOTPSelfServiceAndLogin(t *testing.T) {
	s := passwordTestServer(t)
	cookie, codes, secret := totpEnroll(t, s)
	status := totpJSON(t, totpRequest(s, "GET", "/api/totp", nil, cookie))
	if status["enabled"] != true || status["recoveryCodesRemaining"] != float64(10) {
		t.Fatal(status)
	}
	login := passwordPost(s, "/api/password/login", passwordRequest{Email: "user@example.com", Password: "a long test password"})
	if totpJSON(t, login)["mfaRequired"] != true {
		t.Fatal("password bypass")
	}
	pending := totpResponseCookie(t, login, s.cfg.CookieName+"_totp")
	if !pending.HttpOnly || pending.Path != "/" || pending.Domain != "" {
		t.Fatal("unsafe challenge cookie")
	}
	if w := totpRequest(s, "GET", "/api/tabs", nil, pending); w.Code != 401 {
		t.Fatal("pending challenge accessed workspace", w.Code)
	}
	// The enrollment step has already been consumed, including a separate primary flow.
	same, _ := totp.GenerateCode(secret, time.Now())
	if w := totpRequest(s, "POST", "/api/totp/verify", map[string]string{"code": same}, pending); w.Code != 401 {
		t.Fatal("replayed enrollment step", w.Code)
	}
	verified := totpRequest(s, "POST", "/api/totp/verify", map[string]string{"code": codes[0]}, pending)
	totpJSON(t, verified)
	current := totpResponseCookie(t, verified, s.cfg.CookieName)
	if w := totpRequest(s, "POST", "/api/totp/verify", map[string]string{"code": codes[1]}, pending); w.Code != 409 {
		t.Fatal("replayed challenge", w.Code)
	}
	// A valid primary credential, used by the passkey adapter, must also stop at MFA.
	w := httptest.NewRecorder()
	mfa, err := s.establishLogin(w, httptest.NewRequest("POST", "/", nil), &authkit.Identity{UserID: "user", Email: "user@example.com"}, nil)
	if err != nil || !mfa {
		t.Fatal("primary adapter bypass", err)
	}
	regenerated := totpRequest(s, "POST", "/api/totp/recovery", map[string]string{"code": codes[1]}, current)
	newCodes := totpJSON(t, regenerated)["recoveryCodes"].([]any)
	current = totpResponseCookie(t, regenerated, s.cfg.CookieName)
	if w := totpRequest(s, "GET", "/api/totp", nil, cookie); w.Code != 401 {
		t.Fatal("regeneration retained old sessions")
	}
	if w := totpRequest(s, "POST", "/api/totp/disable", map[string]string{"code": codes[2]}, current); w.Code != 401 {
		t.Fatal("old recovery set accepted", w.Code)
	}
	disabled := totpRequest(s, "POST", "/api/totp/disable", map[string]string{"code": newCodes[0].(string)}, current)
	if totpJSON(t, disabled)["enabled"] != false {
		t.Fatal("disable failed")
	}
	if totpJSON(t, passwordPost(s, "/api/password/login", passwordRequest{Email: "user@example.com", Password: "a long test password"}))["mfaRequired"] == true {
		t.Fatal("disabled factor required")
	}
}
func TestTOTPRecoveryCannotBypassFactor(t *testing.T) {
	s := passwordTestServer(t)
	_, codes, _ := totpEnroll(t, s)
	login := passwordPost(s, "/api/password/login", passwordRequest{Email: "user@example.com", Password: "a long test password"})
	pending := totpResponseCookie(t, login, s.cfg.CookieName+"_totp")
	invite, err := s.store.Invite("user@example.com", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	recovered := passwordPost(s, "/api/password/register", passwordRequest{Email: "user@example.com", Password: "a new long test password", Token: invite})
	if totpJSON(t, recovered)["mfaRequired"] != true {
		t.Fatal("password recovery bypassed 2FA")
	}
	if w := totpRequest(s, "POST", "/api/totp/verify", map[string]string{"code": codes[0]}, pending); w.Code != 409 {
		t.Fatal("old password challenge survived recovery", w.Code)
	}
}
func TestTOTPCodeVectorsAndEncryption(t *testing.T) {
	secret := base32.StdEncoding.EncodeToString([]byte("12345678901234567890"))
	for _, v := range []struct {
		at   int64
		code string
	}{{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"}} {
		step, err := checkTOTPCode(secret, v.code, time.Unix(v.at, 0), -1)
		if err != nil || step != v.at/30 {
			t.Fatal("RFC6238 vector", v.at, err)
		}
		if _, err = checkTOTPCode(secret, v.code, time.Unix(v.at, 0), step); !errors.Is(err, store.ErrTOTPCode) {
			t.Fatal("replay accepted")
		}
	}
	now := time.Unix(1234567890, 0)
	for _, offset := range []int64{-30, 30} {
		code, _ := totp.GenerateCode(secret, now.Add(time.Duration(offset)*time.Second))
		if _, err := checkTOTPCode(secret, code, now, -1); err != nil {
			t.Fatal("clock skew", err)
		}
	}
	code, _ := totp.GenerateCode(secret, now.Add(90*time.Second))
	if _, err := checkTOTPCode(secret, code, now, -1); err == nil {
		t.Fatal("large clock skew accepted")
	}
	s := passwordTestServer(t)
	ciphertext := s.sealTOTP("user@example.com", secret)
	if _, err := openTOTP(s.totpCipher, "other@example.com", ciphertext); err == nil {
		t.Fatal("seed crossed accounts")
	}
	if err := s.store.StartTOTPSetup("user@example.com", "setup", ciphertext); err != nil {
		t.Fatal(err)
	}
	// Persisted key survives a change in the authentication signing key.
	aead, err := loadTOTPCipher(s.cfg.DataDir, []byte("rotated-session-key"), s.store)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := openTOTP(aead, "user@example.com", ciphertext); err != nil || got != secret {
		t.Fatal("key rotation lost seed", err)
	}
	if err := os.WriteFile(filepath.Join(s.cfg.DataDir, "totp.key"), make([]byte, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTOTPCipher(s.cfg.DataDir, []byte("rotated-session-key"), s.store); err == nil {
		t.Fatal("incorrect key accepted")
	}
}
func TestTOTPAuthenticationStorageFailure(t *testing.T) {
	s := passwordTestServer(t)
	cookie, _, _ := totpEnroll(t, s)
	_ = s.store.Close()
	for _, path := range []string{"/api/totp", "/", "/login"} {
		if w := totpRequest(s, "GET", path, nil, cookie); w.Code != 503 {
			t.Fatalf("%s storage error became %d", path, w.Code)
		}
	}
}

func TestTOTPOIDCAndExternalExchangeRequireSecondStep(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.server.Close()
	s := newSSOTestServer(t, idp, []string{"operator@example.com"})
	const email = "operator@example.com"
	identity := &authkit.Identity{UserID: "external-user", Email: email}
	primaryToken, err := s.mintSessionWithFactors(identity, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	secret := base32.StdEncoding.EncodeToString([]byte("12345678901234567890"))
	if err = s.store.StartTOTPSetup(email, "setup", s.sealTOTP(email, secret)); err != nil {
		t.Fatal(err)
	}
	code, _ := totp.GenerateCode(secret, time.Now())
	if _, err = s.store.ConfirmTOTPSetup(email, "setup", []string{recoveryHash("12345678-12345678-12345678-12345678")}, s.totpCheck(email, code)); err != nil {
		t.Fatal(err)
	}
	idp.idToken = idp.sign(email, "n", nil)
	callback := runSSOCallback(t, s)
	if callback.Header().Get("Location") != "/login?step=totp" {
		t.Fatal("OIDC bypassed factor", callback.Header().Get("Location"))
	}
	_ = totpResponseCookie(t, callback, s.cfg.CookieName+"_totp")
	for _, c := range callback.Result().Cookies() {
		if c.Name == s.cfg.CookieName && c.Value != "" {
			t.Fatal("OIDC issued full session")
		}
	}
	// Use a verified identity token to exercise the compatibility exchange handler.
	s.verifier = s.sessionVerifier
	body, _ := json.Marshal(map[string]string{"access_token": primaryToken})
	r := httptest.NewRequest("POST", "/api/session", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	s.handleSession(w, r)
	if totpJSON(t, w)["mfaRequired"] != true {
		t.Fatal("external exchange bypassed factor")
	}
}
