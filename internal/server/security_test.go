package server

import (
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLocalPasskeyRequestBoundaries(t *testing.T) {
	dir := t.TempDir()
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target, _ := url.Parse("http://127.0.0.1:1")
	s, err := New(Config{AuthMode: "local", PublicURL: "http://localhost:18769", Listen: "127.0.0.1:0", DataDir: dir, DatabaseURL: filepath.Join(dir, "workspace.db"), CookieName: "shell_session", AllowedEmails: map[string]struct{}{"user@example.com": {}}, Machines: []MachineConfig{{Name: "default", URL: target}}, SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown() }()
	if runtime.GOOS != "windows" {
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatal("data directory was not protected")
		}
	}
	for _, tc := range []struct {
		name, host, origin, path string
		status                   int
	}{
		{"valid local ceremony", "localhost:18769", "http://localhost:18769", "/api/passkeys/login/begin", 200},
		{"wrong host", "outside.example.com", "http://localhost:18769", "/api/passkeys/login/begin", 421},
		{"wrong origin", "localhost:18769", "https://outside.example.com", "/api/passkeys/login/begin", 403},
		{"external exchange disabled", "localhost:18769", "http://localhost:18769", "/api/session", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://"+tc.host+tc.path, strings.NewReader("{}"))
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.httpSrv.Handler.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		})
	}
}
