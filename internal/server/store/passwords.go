package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
)

// PasswordState is private authentication data, never a browser response.
func (s *Store) PasswordState(email string) (hash, version string, err error) {
	err = s.db.QueryRow(s.q(`SELECT password_hash, version FROM account_passwords WHERE email = ?`), normalizeEmail(email)).Scan(&hash, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return
}

// EnrollPassword atomically consumes an invitation, sets the credential,
// invalidates previous sessions and records the actor trail.
func (s *Store) EnrollPassword(u *PasskeyUser, token, hash string) error {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = s.consumeEnrollment(tx, u, token); err != nil {
		return err
	}
	if _, err = tx.Exec(s.q(`INSERT INTO account_passwords (email, password_hash, version) VALUES (?, ?, ?) ON CONFLICT (email) DO UPDATE SET password_hash = excluded.password_hash, version = excluded.version`), u.Email, hash, rand.Text()); err != nil {
		return err
	}
	if err = s.audit(tx, u.Email, "account.password.set", u.Email); err != nil {
		return err
	}
	return tx.Commit()
}
