package server

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func loadSessionKey(dir string) ([]byte, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "session.key")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_, writeErr := f.WriteString(rand.Text() + rand.Text())
		err = errors.Join(writeErr, f.Close())
		if err != nil {
			return nil, fmt.Errorf("create session key: %w", err)
		}
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(key) < 32 {
		return nil, fmt.Errorf("session key must have at least 32 bytes")
	}
	return key, nil
}

func (s *Server) requestOrigin(r *http.Request) string {
	if s.cfg.PublicURL != "" && !s.cfg.EdgeRoutingEnabled() {
		return s.cfg.PublicURL
	}
	scheme := "https"
	if !s.cfg.CookieSecure {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}

func (s *Server) protectRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if (s.cfg.AuthMode == "local" || s.cfg.AuthMode == "sso") && r.URL.Path != "/healthz" {
			if s.cfg.EdgeRoutingEnabled() {
				if !s.cfg.IsApexHost(r.Host) && !s.cfg.IsEdgeHost(r.Host) {
					http.Error(w, "Unrecognized site address", http.StatusMisdirectedRequest)
					return
				}
			} else {
				u, _ := url.Parse(s.cfg.PublicURL)
				if u == nil || !strings.EqualFold(r.Host, u.Host) {
					http.Error(w, "Unrecognized site address", http.StatusMisdirectedRequest)
					return
				}
			}
		}
		websocket := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
		write := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
		if websocket || write {
			origin := r.Header.Get("Origin")
			if (origin != "" && origin != s.requestOrigin(r)) || (websocket && origin == "") || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				http.Error(w, "Cross-site request denied", http.StatusForbidden)
				return
			}
			if !strings.HasPrefix(r.URL.Path, "/t/") {
				r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
				if r.ContentLength != 0 && !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
					http.Error(w, "Expected application/json", http.StatusUnsupportedMediaType)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
