package server

import (
	authkit "github.com/x0c/shell-gate/internal/auth"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type authBucket struct {
	count   int
	expires time.Time
}
type passwordGuard struct {
	mu        sync.Mutex
	requests  map[string]authBucket
	work      chan struct{}
	dummyHash string
}

func newPasswordGuard() (*passwordGuard, error) {
	hash, err := authkit.HashPassword("dummy-credential-never-accepted")
	if err != nil {
		return nil, err
	}
	return &passwordGuard{requests: make(map[string]authBucket), work: make(chan struct{}, 2), dummyHash: hash}, nil
}

func (g *passwordGuard) allow(peer string) bool {
	host, _, err := net.SplitHostPort(peer)
	if err != nil {
		host = peer
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	for key, v := range g.requests {
		if !v.expires.After(now) {
			delete(g.requests, key)
		}
	}
	bucket, ok := g.requests[host]
	if !ok {
		if len(g.requests) >= 1024 {
			return false
		}
		bucket.expires = now.Add(time.Minute)
	}
	if bucket.count >= 30 {
		return false
	}
	bucket.count++
	g.requests[host] = bucket
	return true
}

func (s *Server) passwordWork(w http.ResponseWriter, r *http.Request) bool {
	if !s.passwords.allow(r.RemoteAddr) {
		w.Header().Set("Retry-After", "60")
		writeJSON(w, 429, map[string]string{"message": "Too many sign-in requests. Try again later."})
		return false
	}
	select {
	case s.passwords.work <- struct{}{}:
		return true
	default:
		writeJSON(w, 503, map[string]string{"message": "Sign-in is busy. Please try again."})
		return false
	}
}

type passwordRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Token    string `json:"token"`
}

func (s *Server) handlePasswordRegister(w http.ResponseWriter, r *http.Request) {
	var req passwordRequest
	if readJSON(r, &req) != nil {
		writeJSON(w, 400, map[string]string{"message": "Invalid request"})
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if !authkit.ValidPassword(req.Password) {
		writeJSON(w, 400, map[string]string{"message": "Use a password with 12 to 128 characters."})
		return
	}
	if !s.passwordWork(w, r) {
		return
	}
	defer func() { <-s.passwords.work }()
	u, err := s.store.EnrollmentUser(req.Email, req.Token)
	if err != nil || !s.cfg.emailAllowed(req.Email) {
		writeJSON(w, 403, map[string]string{"message": "Invitation is invalid, expired, or already used."})
		return
	}
	hash, err := authkit.HashPassword(req.Password)
	if err != nil || s.store.EnrollPassword(u, req.Token, hash) != nil {
		writeJSON(w, 400, map[string]string{"message": "Could not set up this account. Request a new invitation."})
		return
	}
	s.completeLocalLogin(w, r, u)
}

func (s *Server) handlePasswordLogin(w http.ResponseWriter, r *http.Request) {
	var req passwordRequest
	if readJSON(r, &req) != nil {
		writeJSON(w, 400, map[string]string{"message": "Invalid request"})
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if !s.passwordWork(w, r) {
		return
	}
	defer func() { <-s.passwords.work }()
	hash, version, err := s.store.PasswordState(req.Email)
	if err != nil {
		writeJSON(w, 503, map[string]string{"message": "Sign-in is unavailable. Please try again."})
		return
	}
	exists := hash != ""
	if !exists {
		hash = s.passwords.dummyHash
	}
	match := authkit.CheckPassword(hash, req.Password)
	if !exists || !match || !s.cfg.emailAllowed(req.Email) {
		writeJSON(w, 401, map[string]string{"message": "Email or password is incorrect."})
		return
	}
	u, err := s.store.PasskeyUser(req.Email)
	if err != nil || u == nil {
		writeJSON(w, 503, map[string]string{"message": "Sign-in is unavailable. Please try again."})
		return
	}
	s.completeLocalLoginWithVersion(w, r, u, &version)
}
