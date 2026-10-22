package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	authkit "github.com/x0c/shell-gate/internal/auth"
	"github.com/x0c/shell-gate/internal/server/store"
)

type sessionRequest struct {
	AccessToken string `json:"access_token"`
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if s.verifier == nil {
		http.NotFound(w, r)
		return
	}
	defer func() {
		if err := r.Body.Close(); err != nil {
			log.Printf("关闭会话请求正文失败: %v", err)
		}
	}()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Could not read request"})
		return
	}
	var req sessionRequest
	if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.AccessToken) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Missing access token"})
		return
	}
	id, err := s.verifier.Verify(req.AccessToken)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Token is invalid or expired"})
		return
	}
	if !s.cfg.emailAllowed(id.Email) {
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "Account is not allowed"})
		return
	}
	mfa, err := s.establishLogin(w, r, id, nil)
	if err != nil {
		s.totpError(w, err, "primary")
		return
	}
	if mfa {
		writeJSON(w, 200, map[string]any{"status": "mfa_required", "mfaRequired": true})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "email": id.Email})
}

// Verified credential and factor versions must not be upgraded by concurrent recovery.
func (s *Server) mintSessionWithFactors(id *authkit.Identity, verifiedVersion, factorVersion *string) (string, error) {
	if id == nil || id.UserID == "" {
		return "", fmt.Errorf("缺少用户身份")
	}
	email := strings.TrimSpace(id.Email)
	if email == "" {
		return "", fmt.Errorf("缺少邮箱身份")
	}
	if len(s.sessionSecret) == 0 || s.sessionIssuer == "" {
		return "", fmt.Errorf("会话签发配置不完整")
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":   s.sessionIssuer,
		"aud":   s.sessionAudience,
		"sub":   id.UserID,
		"email": email,
		"iat":   now.Unix(),
		"exp":   now.Add(s.cfg.SessionTTL).Unix(),
	}
	if s.cfg.AuthMode == "local" && s.store != nil {
		_, version, err := s.store.PasswordState(email)
		if err != nil {
			return "", err
		}
		if verifiedVersion != nil {
			version = *verifiedVersion
		}
		claims["pv"] = version
	}
	if s.store != nil {
		state, err := s.store.TOTPState(email)
		if err != nil {
			return "", err
		}
		if state.Enabled() && factorVersion == nil {
			return "", store.ErrTOTPState
		}
		if factorVersion != nil && *factorVersion != state.Version {
			return "", store.ErrTOTPState
		}
		claims["mv"] = state.Version
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(s.sessionSecret)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, s.sessionCookie("", -1))
	if _, err := r.Cookie(s.cfg.CookieName + "_totp"); err == nil {
		http.SetCookie(w, s.totpCookie("", -1))
	}
	loginURL := "/login"
	if s.cfg.EdgeRoutingEnabled() {
		// 边缘上登出后必须回 apex；相对 /login 在边缘会被再 302，但 Cookie 清在边缘域也需要 Domain 对齐。
		loginURL = s.cfg.ApexOrigin() + "/login"
	}
	if r.Method == http.MethodGet {
		http.Redirect(w, r, loginURL, http.StatusFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// sessionCookie 构造会话 Cookie；Domain 与签发/清除必须一致，否则边缘清不掉 apex 写下的会话。
func (s *Server) sessionCookie(value string, maxAge int) *http.Cookie {
	c := &http.Cookie{
		Name:     s.cfg.CookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	}
	if s.cfg.CookieDomain != "" {
		c.Domain = s.cfg.CookieDomain
	}
	return c
}

func (s *Server) requireIdentity(w http.ResponseWriter, r *http.Request) (*authkit.Identity, bool) {
	id, err := s.identityFromCookieErr(r)
	if errors.Is(err, authkit.ErrInvalidToken) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"code": "signin_required", "message": "Sign-in required"})
		return nil, false
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "Authentication is unavailable. Please try again."})
		return nil, false
	}
	return id, true
}

func (s *Server) identityFromCookie(r *http.Request) (*authkit.Identity, bool) {
	id, err := s.identityFromCookieErr(r)
	return id, err == nil
}

func (s *Server) identityFromCookieErr(r *http.Request) (*authkit.Identity, error) {
	c, err := r.Cookie(s.cfg.CookieName)
	if err != nil || c.Value == "" {
		return nil, authkit.ErrInvalidToken
	}
	v := s.sessionVerifier
	if v == nil {
		v = s.verifier
	}
	if v == nil {
		return nil, authkit.ErrInvalidToken
	}
	id, err := v.Verify(c.Value)
	if err != nil || !s.cfg.emailAllowed(id.Email) {
		return nil, authkit.ErrInvalidToken
	}
	if s.cfg.AuthMode == "local" && s.store != nil {
		_, version, err := s.store.PasswordState(id.Email)
		if err != nil {
			return nil, err
		}
		tokenVersion, _ := id.Claims["pv"].(string)
		if tokenVersion != version {
			return nil, authkit.ErrInvalidToken
		}
	}
	if s.store != nil {
		state, err := s.store.TOTPState(id.Email)
		if err != nil {
			return nil, err
		}
		if identityFactorVersion(id) != state.Version {
			return nil, authkit.ErrInvalidToken
		}
	}
	return id, nil
}
