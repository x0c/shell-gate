package server

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"image/png"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	authkit "github.com/x0c/shell-gate/internal/auth"
	"github.com/x0c/shell-gate/internal/server/store"
)

func totpAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}
func openTOTP(aead cipher.AEAD, email, encoded string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", errors.New("invalid encrypted authenticator")
	}
	plain, err := aead.Open(nil, nil, raw, []byte(strings.ToLower(email)))
	if err != nil {
		return "", errors.New("could not decrypt authenticator; restore the matching totp.key")
	}
	return string(plain), nil
}
func loadTOTPCipher(dir string, seed []byte, st *store.Store) (cipher.AEAD, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "totp.key")
	key, err := os.ReadFile(path)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return nil, err
	}
	if missing {
		mac := hmac.New(sha256.New, seed)
		_, _ = mac.Write([]byte("shellgate-totp-storage:v1"))
		key = mac.Sum(nil)
	}
	if len(key) != 32 {
		return nil, errors.New("totp.key must contain exactly 32 bytes")
	}
	aead, err := totpAEAD(key)
	if err != nil {
		return nil, err
	}
	email, sample, err := st.TOTPKeySample()
	if err != nil {
		return nil, err
	}
	if sample != "" {
		if _, err := openTOTP(aead, email, sample); err != nil {
			return nil, err
		}
	}
	if missing {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, os.ErrExist) {
			return loadTOTPCipher(dir, seed, st)
		}
		if err != nil {
			return nil, err
		}
		_, writeErr := f.Write(key)
		if err := errors.Join(writeErr, f.Close()); err != nil {
			return nil, err
		}
	}
	if err := os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	return aead, nil
}
func (s *Server) sealTOTP(email, secret string) string {
	return base64.RawURLEncoding.EncodeToString(s.totpCipher.Seal(nil, nil, []byte(secret), []byte(strings.ToLower(email))))
}
func checkTOTPCode(secret, code string, now time.Time, last int64) (int64, error) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return 0, store.ErrTOTPCode
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return 0, store.ErrTOTPCode
		}
	}
	matched := int64(-1)
	for step := now.Unix()/30 - 1; step <= now.Unix()/30+1; step++ {
		expected, err := totp.GenerateCodeCustom(secret, time.Unix(step*30, 0), totp.ValidateOpts{Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if err != nil {
			return 0, errors.New("invalid authenticator secret")
		}
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 && step > last {
			matched = step
		}
	}
	if matched < 0 {
		return 0, store.ErrTOTPCode
	}
	return matched, nil
}
func (s *Server) totpCheck(email, code string) store.TOTPCheck {
	now := time.Now()
	return func(encrypted string, last int64) (int64, error) {
		secret, err := openTOTP(s.totpCipher, email, encrypted)
		if err != nil {
			return 0, err
		}
		return checkTOTPCode(secret, code, now, last)
	}
}
func recoveryHash(code string) string {
	clean := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(code), "-", ""), " ", ""))
	if len(clean) != 32 {
		return ""
	}
	if _, err := hex.DecodeString(clean); err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte("shellgate-totp-recovery:v1:" + clean))
	return hex.EncodeToString(sum[:])
}
func newRecoveryCodes() (codes, hashes []string) {
	for range 10 {
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			panic("secure randomness unavailable")
		}
		value := hex.EncodeToString(raw)
		code := value[:8] + "-" + value[8:16] + "-" + value[16:24] + "-" + value[24:]
		codes = append(codes, code)
		hashes = append(hashes, recoveryHash(code))
	}
	return
}
func challengeHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func (s *Server) totpCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: s.cfg.CookieName + "_totp", Value: value, Path: "/", HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}
func (s *Server) pendingTOTP(r *http.Request) (*store.TOTPChallenge, error) {
	cookie, err := r.Cookie(s.cfg.CookieName + "_totp")
	if err != nil || len(cookie.Value) > 128 {
		return nil, store.ErrTOTPState
	}
	ch, err := s.store.TOTPChallenge(challengeHash(cookie.Value))
	if err != nil {
		return nil, err
	}
	if ch.ExpiresAt <= time.Now().Unix() {
		return nil, store.ErrTOTPState
	}
	return ch, nil
}

// establishLogin is shared by every primary authentication adapter. Pending
// second-factor cookies can never be parsed as gateway sessions.
func (s *Server) establishLogin(w http.ResponseWriter, r *http.Request, id *authkit.Identity, passwordVersion *string) (bool, error) {
	state, err := s.store.TOTPState(id.Email)
	if err != nil {
		return false, err
	}
	if state.Enabled() {
		pv := ""
		if passwordVersion != nil {
			pv = *passwordVersion
		} else if s.cfg.AuthMode == "local" {
			_, pv, err = s.store.PasswordState(id.Email)
			if err != nil {
				return false, err
			}
		}
		token := rand.Text() + rand.Text()
		ch := store.TOTPChallenge{Hash: challengeHash(token), Email: id.Email, UserID: id.UserID, PasswordVersion: pv, FactorVersion: state.Version, PrimaryAt: time.Now().Unix(), ExpiresAt: time.Now().Add(5 * time.Minute).Unix()}
		if err := s.store.CreateTOTPChallenge(ch); err != nil {
			return false, err
		}
		http.SetCookie(w, s.sessionCookie("", -1))
		http.SetCookie(w, s.totpCookie(token, 300))
		return true, nil
	}
	if err := s.issueTOTPVerifiedSession(w, id, passwordVersion, nil); err != nil {
		return false, err
	}
	if _, err := r.Cookie(s.cfg.CookieName + "_totp"); err == nil {
		http.SetCookie(w, s.totpCookie("", -1))
	}
	return false, nil
}
func (s *Server) issueTOTPVerifiedSession(w http.ResponseWriter, id *authkit.Identity, pv, factorVersion *string) error {
	if err := s.store.EnsureDefaults(id.Email); err != nil {
		return err
	}
	token, err := s.mintSessionWithFactors(id, pv, factorVersion)
	if err != nil {
		return err
	}
	http.SetCookie(w, s.sessionCookie(token, int(s.cfg.SessionTTL.Seconds())))
	if factorVersion != nil {
		http.SetCookie(w, s.totpCookie("", -1))
	}
	return nil
}
func (s *Server) totpError(w http.ResponseWriter, err error, stage string) {
	switch {
	case errors.Is(err, store.ErrTOTPCode):
		writeJSON(w, 401, map[string]string{"message": "That code is invalid or already used. Use a new authenticator code or an unused recovery code."})
	case errors.Is(err, store.ErrTOTPLocked), errors.Is(err, store.ErrTOTPBusy):
		w.Header().Set("Retry-After", "300")
		writeJSON(w, 429, map[string]string{"message": "Too many attempts. Try again in five minutes."})
	case errors.Is(err, store.ErrTOTPState):
		writeJSON(w, 409, map[string]string{"message": "This verification expired or changed. Start again."})
	default:
		log.Printf("TOTP operation failed: stage=%s", stage)
		writeJSON(w, 503, map[string]string{"message": "Account security is unavailable. Please try again."})
	}
}
func (s *Server) recentSecurityIdentity(w http.ResponseWriter, r *http.Request) (*authkit.Identity, bool) {
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return nil, false
	}
	at, err := id.Claims.GetIssuedAt()
	if err != nil || at == nil || time.Since(at.Time) > 5*time.Minute || at.After(time.Now().Add(time.Minute)) {
		writeJSON(w, 403, map[string]string{"code": "reauth_required", "message": "Sign in again to change account security."})
		return nil, false
	}
	return id, true
}
func identityPasswordVersion(id *authkit.Identity) *string {
	value, _ := id.Claims["pv"].(string)
	return &value
}
func identityFactorVersion(id *authkit.Identity) string {
	value, _ := id.Claims["mv"].(string)
	return value
}
func (s *Server) handleTOTPStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	state, err := s.store.TOTPState(id.Email)
	if err != nil {
		s.totpError(w, err, "status")
		return
	}
	count, err := s.store.TOTPRecoveryCount(id.Email)
	if err != nil {
		s.totpError(w, err, "status")
		return
	}
	writeJSON(w, 200, map[string]any{"enabled": state.Enabled(), "recoveryCodesRemaining": count, "email": id.Email})
}
func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	id, ok := s.recentSecurityIdentity(w, r)
	if !ok {
		return
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "ShellGate", AccountName: id.Email, SecretSize: 20, Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		s.totpError(w, err, "setup")
		return
	}
	image, err := key.Image(240, 240)
	if err != nil {
		s.totpError(w, err, "setup")
		return
	}
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image); err != nil {
		s.totpError(w, err, "setup")
		return
	}
	setupID := rand.Text()
	if err := s.store.StartTOTPSetup(id.Email, setupID, s.sealTOTP(id.Email, key.Secret())); err != nil {
		s.totpError(w, err, "setup")
		return
	}
	writeJSON(w, 200, map[string]string{"setupId": setupID, "secret": key.Secret(), "uri": key.URL(), "qr": "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngData.Bytes())})
}
func (s *Server) handleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	id, ok := s.recentSecurityIdentity(w, r)
	if !ok {
		return
	}
	var req struct {
		SetupID string `json:"setupId"`
		Code    string `json:"code"`
	}
	if readJSON(r, &req) != nil || len(req.Code) > 128 {
		writeJSON(w, 400, map[string]string{"message": "Enter an authenticator code."})
		return
	}
	codes, hashes := newRecoveryCodes()
	version, err := s.store.ConfirmTOTPSetup(id.Email, req.SetupID, hashes, s.totpCheck(id.Email, req.Code))
	if err != nil {
		s.totpError(w, err, "enable")
		return
	}
	if err := s.issueTOTPVerifiedSession(w, id, identityPasswordVersion(id), &version); err != nil {
		s.totpError(w, err, "session")
		return
	}
	writeJSON(w, 200, map[string]any{"enabled": true, "recoveryCodes": codes})
}
func (s *Server) handleTOTPChange(w http.ResponseWriter, r *http.Request) {
	id, ok := s.recentSecurityIdentity(w, r)
	if !ok {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if readJSON(r, &req) != nil || len(req.Code) > 128 {
		writeJSON(w, 400, map[string]string{"message": "Enter an authenticator or recovery code."})
		return
	}
	disable := r.URL.Path == "/api/totp/disable"
	var codes, hashes []string
	if !disable {
		codes, hashes = newRecoveryCodes()
	}
	version, err := s.store.ChangeTOTP(id.Email, identityFactorVersion(id), recoveryHash(req.Code), hashes, disable, s.totpCheck(id.Email, req.Code))
	if err != nil {
		s.totpError(w, err, "change")
		return
	}
	if err := s.issueTOTPVerifiedSession(w, id, identityPasswordVersion(id), &version); err != nil {
		s.totpError(w, err, "session")
		return
	}
	writeJSON(w, 200, map[string]any{"enabled": !disable, "recoveryCodes": codes})
}
func (s *Server) handleTOTPPending(w http.ResponseWriter, r *http.Request) {
	_, err := s.pendingTOTP(r)
	if errors.Is(err, store.ErrTOTPState) {
		writeJSON(w, 200, map[string]bool{"required": false})
		return
	}
	if err != nil {
		s.totpError(w, err, "pending")
		return
	}
	writeJSON(w, 200, map[string]bool{"required": true})
}
func (s *Server) handleTOTPVerify(w http.ResponseWriter, r *http.Request) {
	pending, err := s.pendingTOTP(r)
	if err != nil {
		s.totpError(w, err, "pending")
		return
	}
	if !s.cfg.emailAllowed(pending.Email) {
		writeJSON(w, 403, map[string]string{"message": "Account is not allowed."})
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if readJSON(r, &req) != nil || len(req.Code) > 128 {
		writeJSON(w, 400, map[string]string{"message": "Enter a verification code."})
		return
	}
	ch, err := s.store.ConsumeTOTPChallenge(pending.Hash, recoveryHash(req.Code), s.totpCheck(pending.Email, req.Code))
	if err != nil {
		s.totpError(w, err, "verify")
		return
	}
	id := &authkit.Identity{UserID: ch.UserID, Email: ch.Email}
	if err := s.issueTOTPVerifiedSession(w, id, &ch.PasswordVersion, &ch.FactorVersion); err != nil {
		s.totpError(w, err, "session")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
