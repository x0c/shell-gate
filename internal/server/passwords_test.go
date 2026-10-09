package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func passwordTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	target, _ := url.Parse("http://127.0.0.1:1")
	s, err := New(Config{AuthMode: "local", PublicURL: "http://localhost:18769", Listen: "127.0.0.1:0", DataDir: dir, DatabaseURL: filepath.Join(dir, "workspace.db"), CookieName: "shell_session", AllowedEmails: map[string]struct{}{"user@example.com": {}}, Machines: []MachineConfig{{Name: "default", URL: target}}, SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown() })
	return s
}

func passwordPost(s *Server, path string, req passwordRequest) *httptest.ResponseRecorder {
	body, _ := json.Marshal(req)
	r := httptest.NewRequest("POST", "http://localhost:18769"+path, strings.NewReader(string(body)))
	r.Header.Set("Origin", "http://localhost:18769")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpSrv.Handler.ServeHTTP(w, r)
	return w
}

func TestPasswordLoginRecoveryAndVersionRace(t *testing.T) {
	s := passwordTestServer(t)
	invite, err := s.store.Invite("user@example.com", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	req := passwordRequest{Email: "user@example.com", Password: "a long test password", Token: invite}
	registered := passwordPost(s, "/api/password/register", req)
	if registered.Code != 200 || len(registered.Result().Cookies()) != 1 {
		t.Fatalf("registration: %d %s", registered.Code, registered.Body.String())
	}
	oldCookie := registered.Result().Cookies()[0]
	if replay := passwordPost(s, "/api/password/register", req); replay.Code != 403 {
		t.Fatalf("invitation replay: %d", replay.Code)
	}
	wrong := passwordPost(s, "/api/password/login", passwordRequest{Email: req.Email, Password: "wrong long password"})
	unknown := passwordPost(s, "/api/password/login", passwordRequest{Email: "unknown@example.com", Password: "wrong long password"})
	if wrong.Code != 401 || unknown.Code != 401 || wrong.Body.String() != unknown.Body.String() {
		t.Fatal("login failure disclosed account existence")
	}
	if login := passwordPost(s, "/api/password/login", req); login.Code != 200 {
		t.Fatalf("password login: %d", login.Code)
	}
	_, oldVersion, err := s.store.PasswordState(req.Email)
	if err != nil {
		t.Fatal(err)
	}
	req.Token, err = s.store.Invite(req.Email, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	req.Password = "a recovered test password"
	if reset := passwordPost(s, "/api/password/register", req); reset.Code != 200 {
		t.Fatalf("password recovery: %d", reset.Code)
	}
	checkCookie := func(cookie *http.Cookie) bool {
		r := httptest.NewRequest("GET", "http://localhost:18769/api/tabs", nil)
		r.AddCookie(cookie)
		_, ok := s.identityFromCookie(r)
		return ok
	}
	if checkCookie(oldCookie) {
		t.Fatal("old session survived recovery")
	}
	u, err := s.store.PasskeyUser(req.Email)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate old-password verification finishing after a concurrent recovery.
	w := httptest.NewRecorder()
	s.completeLocalLoginWithVersion(w, u, &oldVersion)
	if w.Code != 200 || checkCookie(w.Result().Cookies()[0]) {
		t.Fatal("concurrent recovery upgraded an old credential session")
	}
	if old := passwordPost(s, "/api/password/login", passwordRequest{Email: req.Email, Password: "a long test password"}); old.Code != 401 {
		t.Fatal("old password survived recovery")
	}
	current := passwordPost(s, "/api/password/login", req)
	if current.Code != 200 || !checkCookie(current.Result().Cookies()[0]) {
		t.Fatal("recovered password did not sign in")
	}
}

func TestPasswordRequestRateAndWorkBounds(t *testing.T) {
	s := passwordTestServer(t)
	for i := 0; i < 30; i++ {
		if !s.passwords.allow("203.0.113.1:1234") {
			t.Fatal("unexpected rate rejection")
		}
	}
	if s.passwords.allow("203.0.113.1:4321") {
		t.Fatal("rate limit bypassed by changing source port")
	}
	s.passwords.work <- struct{}{}
	s.passwords.work <- struct{}{}
	w := passwordPost(s, "/api/password/login", passwordRequest{Email: "user@example.com", Password: "a long test password"})
	if w.Code != 503 {
		t.Fatalf("unbounded password work: %d", w.Code)
	}
}
