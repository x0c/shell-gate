package server

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	authkit "github.com/x0c/shell-gate/internal/auth"
	"github.com/x0c/shell-gate/internal/server/store"
)

const ceremonyTTL = 5 * time.Minute
const maxCeremonies = 1024

type ceremony struct {
	session webauthn.SessionData
	user    *store.PasskeyUser
	token   string
	kind    string
	expires time.Time
}

// localPasskeys is the local authentication adapter. Ceremony state is bounded,
// short-lived, browser-bound and consumed before verification, including errors.
type localPasskeys struct {
	wa      *webauthn.WebAuthn
	mu      sync.Mutex
	pending map[string]ceremony
}

func newLocalPasskeys(origin string) (*localPasskeys, error) {
	u, err := url.Parse(origin)
	if err != nil {
		return nil, err
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPDisplayName: "ShellGate", RPID: u.Hostname(), RPOrigins: []string{origin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{UserVerification: protocol.VerificationRequired, ResidentKey: protocol.ResidentKeyRequirementRequired},
	})
	if err != nil {
		return nil, err
	}
	return &localPasskeys{wa: wa, pending: make(map[string]ceremony)}, nil
}

func (s *Server) saveCeremony(w http.ResponseWriter, r *http.Request, c ceremony) error {
	p := s.passkeys
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for k, v := range p.pending {
		if !v.expires.After(now) {
			delete(p.pending, k)
		}
	}
	if old, err := r.Cookie(s.cfg.CookieName + "_challenge"); err == nil {
		delete(p.pending, old.Value)
	}
	if len(p.pending) >= maxCeremonies {
		return fmt.Errorf("too many pending requests")
	}
	key := rand.Text()
	c.expires = now.Add(ceremonyTTL)
	p.pending[key] = c
	http.SetCookie(w, &http.Cookie{Name: s.cfg.CookieName + "_challenge", Value: key, Path: "/api/passkeys/", HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: int(ceremonyTTL.Seconds())})
	return nil
}

func (s *Server) takeCeremony(w http.ResponseWriter, r *http.Request, kind string) (ceremony, bool) {
	cookie, err := r.Cookie(s.cfg.CookieName + "_challenge")
	if err != nil {
		return ceremony{}, false
	}
	http.SetCookie(w, &http.Cookie{Name: cookie.Name, Value: "", Path: "/api/passkeys/", HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	p := s.passkeys
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.pending[cookie.Value]
	delete(p.pending, cookie.Value)
	return c, ok && c.kind == kind && c.expires.After(time.Now())
}

func (s *Server) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
		Token string `json:"token"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]string{"message": "Invalid request"})
		return
	}
	if !s.cfg.emailAllowed(req.Email) {
		writeJSON(w, 403, map[string]string{"message": "Invitation is invalid or expired"})
		return
	}
	u, err := s.store.EnrollmentUser(req.Email, req.Token)
	if err != nil {
		writeJSON(w, 403, map[string]string{"message": "Invitation is invalid or expired"})
		return
	}
	options, session, err := s.passkeys.wa.BeginRegistration(u, webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired))
	if err != nil {
		writeJSON(w, 500, map[string]string{"message": "Could not begin enrollment"})
		return
	}
	if err := s.saveCeremony(w, r, ceremony{session: *session, user: u, token: req.Token, kind: "register"}); err != nil {
		writeJSON(w, 429, map[string]string{"message": "Try again shortly"})
		return
	}
	writeJSON(w, 200, options)
}

func (s *Server) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	c, ok := s.takeCeremony(w, r, "register")
	if !ok || !s.cfg.emailAllowed(c.user.Email) {
		writeJSON(w, 401, map[string]string{"message": "Enrollment expired; start again"})
		return
	}
	cred, err := s.passkeys.wa.FinishRegistration(c.user, c.session, r)
	if err != nil {
		writeJSON(w, 401, map[string]string{"message": "Passkey verification failed"})
		return
	}
	if err := s.store.Enroll(c.user, c.token, cred); err != nil {
		writeJSON(w, 409, map[string]string{"message": "Invitation is expired or already used; request a new invitation"})
		return
	}
	s.completeLocalLogin(w, r, c.user)
}

func (s *Server) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	options, session, err := s.passkeys.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		writeJSON(w, 500, map[string]string{"message": "Could not begin sign-in"})
		return
	}
	if err := s.saveCeremony(w, r, ceremony{session: *session, kind: "login"}); err != nil {
		writeJSON(w, 429, map[string]string{"message": "Try again shortly"})
		return
	}
	writeJSON(w, 200, options)
}

func (s *Server) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	c, ok := s.takeCeremony(w, r, "login")
	if !ok {
		writeJSON(w, 401, map[string]string{"message": "Sign-in expired; try again"})
		return
	}
	var user *store.PasskeyUser
	cred, err := s.passkeys.wa.FinishDiscoverableLogin(func(_, handle []byte) (webauthn.User, error) {
		u, err := s.store.PasskeyUserByHandle(handle)
		if err != nil || u == nil || !s.cfg.emailAllowed(u.Email) {
			return nil, fmt.Errorf("account unavailable")
		}
		user = u
		return u, nil
	}, c.session, r)
	if err != nil || user == nil || cred.Authenticator.CloneWarning {
		writeJSON(w, 401, map[string]string{"message": "Passkey verification failed"})
		return
	}
	if err := s.store.UpdatePasskey(user.Email, cred); err != nil {
		writeJSON(w, 500, map[string]string{"message": "Could not save sign-in"})
		return
	}
	s.completeLocalLogin(w, r, user)
}

func (s *Server) completeLocalLogin(w http.ResponseWriter, r *http.Request, u *store.PasskeyUser) {
	s.completeLocalLoginWithVersion(w, r, u, nil)
}

func (s *Server) completeLocalLoginWithVersion(w http.ResponseWriter, r *http.Request, u *store.PasskeyUser, version *string) {
	id := &authkit.Identity{UserID: base64.RawURLEncoding.EncodeToString(u.ID), Email: u.Email}
	mfa, err := s.establishLogin(w, r, id, version)
	if err != nil {
		s.totpError(w, err, "primary")
		return
	}
	if mfa {
		writeJSON(w, 200, map[string]any{"status": "mfa_required", "mfaRequired": true})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
