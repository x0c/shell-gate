package server

import (
	"database/sql"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPresetStorageErrorsStayPrivate(t *testing.T) {
	s := passwordTestServer(t)
	token, err := s.store.Invite("user@example.com", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	registration := passwordPost(s, "/api/password/register", passwordRequest{Email: "user@example.com", Password: "long test-only password", Token: token})
	if registration.Code != 200 {
		t.Fatalf("registration: %d", registration.Code)
	}
	cookie := registration.Result().Cookies()[0]
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost:18769"+path, strings.NewReader(body))
		r.Header.Set("Origin", "http://localhost:18769")
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		s.httpSrv.Handler.ServeHTTP(w, r)
		return w
	}
	invalid := request("POST", "/api/presets", `{"name":"","command":""}`)
	if invalid.Code != 400 || !strings.Contains(invalid.Body.String(), "Name and command are required") {
		t.Fatal("validation error was not distinct from storage failure")
	}
	// Simulate an unavailable workspace table while identity storage stays intact.
	db, err := sql.Open("sqlite", s.cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.Exec(`DROP TABLE presets`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, path string }{{"POST", "/api/presets"}, {"PUT", "/api/presets/missing"}} {
		w := request(tc.method, tc.path, `{"name":"Shell","command":"echo hello"}`)
		if w.Code != 503 || w.Body.String() != "{\"message\":\"Could not save preset. Please try again.\"}\n" {
			t.Fatalf("storage detail escaped to browser: %d %s", w.Code, w.Body.String())
		}
	}
}
