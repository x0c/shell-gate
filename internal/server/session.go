package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	authkit "github.com/x0c/shell-gate/internal/auth"
)

type sessionRequest struct {
	AccessToken string `json:"access_token"`
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if s.passkeys != nil {
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "无法读取请求体"})
		return
	}
	var req sessionRequest
	if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.AccessToken) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "缺少 access_token"})
		return
	}
	id, err := s.verifier.Verify(req.AccessToken)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "令牌无效或已过期"})
		return
	}
	if !s.cfg.emailAllowed(id.Email) {
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "该账号无权访问此终端"})
		return
	}
	if err := s.store.EnsureDefaults(id.Email); err != nil {
		log.Printf("种默认预置失败 email=%s: %v", id.Email, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "无法初始化预置"})
		return
	}
	// GoTrue access_token 默认约 1h 过期；门禁会话要按 SessionTTL（默认 12h）独立续命，
	// 不能把短寿命 access_token 直接写进 Cookie，否则 MaxAge 还没到就会被 JWT exp 踢下线。
	sessionToken, err := s.mintSessionToken(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "无法签发会话"})
		return
	}
	http.SetCookie(w, s.sessionCookie(sessionToken, int(s.cfg.SessionTTL.Seconds())))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "email": id.Email})
}

// mintSessionToken 在通行密钥登录校验通过后，签发门禁自己的会话 JWT。
func (s *Server) mintSessionToken(id *authkit.Identity) (string, error) {
	return s.mintSessionTokenWithVersion(id, nil)
}

// A verified password version must not be replaced by a concurrent recovery.
func (s *Server) mintSessionTokenWithVersion(id *authkit.Identity, verifiedVersion *string) (string, error) {
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
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(s.sessionSecret)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, s.sessionCookie("", -1))
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
	id, ok := s.identityFromCookie(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "未登录"})
		return nil, false
	}
	return id, true
}

func (s *Server) identityFromCookie(r *http.Request) (*authkit.Identity, bool) {
	c, err := r.Cookie(s.cfg.CookieName)
	if err != nil || c.Value == "" {
		return nil, false
	}
	v := s.sessionVerifier
	if v == nil {
		v = s.verifier
	}
	if v == nil {
		return nil, false
	}
	id, err := v.Verify(c.Value)
	if err != nil {
		return nil, false
	}
	if !s.cfg.emailAllowed(id.Email) {
		return nil, false
	}
	if s.cfg.AuthMode == "local" && s.store != nil {
		_, version, err := s.store.PasswordState(id.Email)
		if err != nil {
			return nil, false
		}
		tokenVersion, _ := id.Claims["pv"].(string)
		if tokenVersion != version {
			return nil, false
		}
	}
	return id, true
}
