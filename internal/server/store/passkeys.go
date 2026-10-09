package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

var ErrEnrollment = errors.New("invitation is invalid, expired, or already used")

// Fixed precision preserves chronological order when compared as SQL text.
const invitationTimeFormat = "2006-01-02T15:04:05.000000000Z"

// PasskeyUser implements the library's user interface without exposing secrets.
type PasskeyUser struct {
	ID          []byte
	Email       string
	Credentials []webauthn.Credential
}

func (u *PasskeyUser) WebAuthnID() []byte                         { return u.ID }
func (u *PasskeyUser) WebAuthnName() string                       { return u.Email }
func (u *PasskeyUser) WebAuthnDisplayName() string                { return u.Email }
func (u *PasskeyUser) WebAuthnCredentials() []webauthn.Credential { return u.Credentials }

func (s *Store) migratePasskeys() error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS passkey_users (email TEXT PRIMARY KEY, user_id TEXT NOT NULL UNIQUE)`,
		`CREATE TABLE IF NOT EXISTS passkey_credentials (id TEXT PRIMARY KEY, email TEXT NOT NULL REFERENCES passkey_users(email), credential TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS account_passwords (email TEXT PRIMARY KEY REFERENCES passkey_users(email), password_hash TEXT NOT NULL, version TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS enrollment_invitations (token_hash TEXT PRIMARY KEY, email TEXT NOT NULL, expires_at TEXT NOT NULL)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func enrollmentHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// Invite returns a one-use capability. Only its hash is persisted.
func (s *Store) Invite(email string, ttl time.Duration) (string, error) {
	if ttl <= 0 || ttl > 24*time.Hour {
		return "", fmt.Errorf("invitation lifetime must be between 0 and 24 hours")
	}
	token := base64.RawURLEncoding.EncodeToString(randBytes())
	_, err := s.mutate("operator", "passkey.invite", normalizeEmail(email),
		`INSERT INTO enrollment_invitations (token_hash, email, expires_at) VALUES (?, ?, ?)`,
		enrollmentHash(token), normalizeEmail(email), time.Now().UTC().Add(ttl).Format(invitationTimeFormat))
	return token, err
}

func randBytes() []byte { return []byte(rand.Text()) }

func (s *Store) EnrollmentUser(email, token string) (*PasskeyUser, error) {
	email = normalizeEmail(email)
	var expires string
	err := s.db.QueryRow(s.q(`SELECT expires_at FROM enrollment_invitations WHERE token_hash = ? AND email = ?`), enrollmentHash(token), email).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrEnrollment
	}
	if err != nil {
		return nil, err
	}
	at, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || !at.After(time.Now()) {
		return nil, ErrEnrollment
	}
	u, err := s.PasskeyUser(email)
	if err != nil {
		return nil, err
	}
	if u == nil {
		u = &PasskeyUser{ID: randBytes(), Email: email}
	}
	return u, nil
}

func (s *Store) PasskeyUser(email string) (*PasskeyUser, error) {
	var encoded string
	email = normalizeEmail(email)
	err := s.db.QueryRow(s.q(`SELECT user_id FROM passkey_users WHERE email = ?`), email).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	id, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	u := &PasskeyUser{ID: id, Email: email}
	rows, err := s.db.Query(s.q(`SELECT credential FROM passkey_credentials WHERE email = ?`), email)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw string
		var cred webauthn.Credential
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &cred); err != nil {
			return nil, err
		}
		u.Credentials = append(u.Credentials, cred)
	}
	return u, rows.Err()
}

func (s *Store) PasskeyUserByHandle(handle []byte) (*PasskeyUser, error) {
	var email string
	err := s.db.QueryRow(s.q(`SELECT email FROM passkey_users WHERE user_id = ?`), base64.RawURLEncoding.EncodeToString(handle)).Scan(&email)
	if err != nil {
		return nil, err
	}
	return s.PasskeyUser(email)
}

// Enroll consumes the invitation and persists the key in a single transaction.
func (s *Store) Enroll(u *PasskeyUser, token string, cred *webauthn.Credential) error {
	raw, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.consumeEnrollment(tx, u, token); err != nil {
		return err
	}
	cid := base64.RawURLEncoding.EncodeToString(cred.ID)
	if _, err := tx.Exec(s.q(`INSERT INTO passkey_credentials (id, email, credential) VALUES (?, ?, ?)`), cid, u.Email, string(raw)); err != nil {
		return err
	}
	if err := s.audit(tx, u.Email, "passkey.enroll", cid); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdatePasskey(email string, cred *webauthn.Credential) error {
	raw, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	cid := base64.RawURLEncoding.EncodeToString(cred.ID)
	n, err := s.mutate(normalizeEmail(email), "passkey.authenticate", cid,
		`UPDATE passkey_credentials SET credential = ? WHERE id = ? AND email = ?`, string(raw), cid, normalizeEmail(email))
	if err == nil && n != 1 {
		return fmt.Errorf("credential no longer exists")
	}
	return err
}

func (s *Store) consumeEnrollment(tx *sql.Tx, u *PasskeyUser, token string) error {
	res, err := tx.Exec(s.q(`DELETE FROM enrollment_invitations WHERE token_hash = ? AND email = ? AND expires_at > ?`),
		enrollmentHash(token), u.Email, time.Now().UTC().Format(invitationTimeFormat))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrEnrollment
	}
	if err := s.audit(tx, u.Email, "passkey.invitation.consume", enrollmentHash(token)); err != nil {
		return err
	}
	uid := base64.RawURLEncoding.EncodeToString(u.ID)
	userResult, err := tx.Exec(s.q(`INSERT INTO passkey_users (email, user_id) VALUES (?, ?) ON CONFLICT (email) DO NOTHING`), u.Email, uid)
	if err != nil {
		return err
	}
	if created, err := userResult.RowsAffected(); err != nil {
		return err
	} else if created > 0 {
		if err := s.audit(tx, u.Email, "passkey.user.create", uid); err != nil {
			return err
		}
	}
	// Concurrent invitations must not attach a key to another user handle.
	var storedID string
	if err := tx.QueryRow(s.q(`SELECT user_id FROM passkey_users WHERE email = ?`), u.Email).Scan(&storedID); err != nil {
		return err
	}
	if storedID != uid {
		return fmt.Errorf("account enrollment changed; start again")
	}
	return nil
}
