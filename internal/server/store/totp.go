package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"time"
)

var (
	ErrTOTPCode   = errors.New("invalid or reused verification code")
	ErrTOTPLocked = errors.New("verification temporarily locked")
	ErrTOTPState  = errors.New("two-factor state changed or expired")
	ErrTOTPBusy   = errors.New("too many pending sign-ins")
)

type TOTPState struct {
	Email        string
	Secret       string `json:"-"`
	Version      string
	LastStep     int64
	Failures     int
	LockedUntil  int64
	SetupID      string
	SetupSecret  string `json:"-"`
	SetupExpires int64
}

func (s TOTPState) Enabled() bool { return s.Secret != "" }

type TOTPChallenge struct {
	Hash            string `json:"-"`
	Email           string
	UserID          string
	PasswordVersion string `json:"-"`
	FactorVersion   string
	PrimaryAt       int64
	ExpiresAt       int64
}

type TOTPCheck func(ciphertext string, lastStep int64) (int64, error)

func (s *Store) migrateTOTP() error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS account_totp (email TEXT PRIMARY KEY, secret TEXT NOT NULL DEFAULT '', version TEXT NOT NULL DEFAULT '', last_step BIGINT NOT NULL DEFAULT -1, failures INTEGER NOT NULL DEFAULT 0, locked_until BIGINT NOT NULL DEFAULT 0, setup_id TEXT NOT NULL DEFAULT '', setup_secret TEXT NOT NULL DEFAULT '', setup_expires BIGINT NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS totp_recovery_codes (email TEXT NOT NULL REFERENCES account_totp(email), code_hash TEXT NOT NULL, PRIMARY KEY (email,code_hash))`,
		`CREATE TABLE IF NOT EXISTS totp_login_challenges (token_hash TEXT PRIMARY KEY, email TEXT NOT NULL REFERENCES account_totp(email), user_id TEXT NOT NULL, password_version TEXT NOT NULL, factor_version TEXT NOT NULL, primary_at BIGINT NOT NULL, expires_at BIGINT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS idx_totp_challenges_expiry ON totp_login_challenges(expires_at)`,
	} {
		if _, err := s.db.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

const totpColumns = `email,secret,version,last_step,failures,locked_until,setup_id,setup_secret,setup_expires`

func scanTOTP(row interface{ Scan(...any) error }) (*TOTPState, error) {
	var state TOTPState
	err := row.Scan(&state.Email, &state.Secret, &state.Version, &state.LastStep, &state.Failures, &state.LockedUntil, &state.SetupID, &state.SetupSecret, &state.SetupExpires)
	return &state, err
}
func (s *Store) TOTPState(email string) (*TOTPState, error) {
	email = normalizeEmail(email)
	state, err := scanTOTP(s.db.QueryRow(s.q(`SELECT `+totpColumns+` FROM account_totp WHERE email=?`), email))
	if errors.Is(err, sql.ErrNoRows) {
		return &TOTPState{Email: email, LastStep: -1}, nil
	}
	return state, err
}
func (s *Store) TOTPKeySample() (email, ciphertext string, err error) {
	err = s.db.QueryRow(`SELECT email,CASE WHEN secret<>'' THEN secret ELSE setup_secret END FROM account_totp WHERE secret<>'' OR setup_secret<>'' LIMIT 1`).Scan(&email, &ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return
}
func (s *Store) TOTPRecoveryCount(email string) (int, error) {
	var count int
	err := s.db.QueryRow(s.q(`SELECT COUNT(*) FROM totp_recovery_codes WHERE email=?`), normalizeEmail(email)).Scan(&count)
	return count, err
}

// The row lock serializes verification, configuration and recovery consumption.
// Invalid-code updates intentionally commit their audit and persistent rate limit.
func (s *Store) withTOTP(email string, create bool, action func(*sql.Tx, *TOTPState) error) error {
	email = normalizeEmail(email)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if create {
		result, err := tx.Exec(s.q(`INSERT INTO account_totp(email) VALUES (?) ON CONFLICT(email) DO NOTHING`), email)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count > 0 {
			if err := s.audit(tx, email, "totp.account.create", email); err != nil {
				return err
			}
		}
	}
	query := `SELECT ` + totpColumns + ` FROM account_totp WHERE email=?`
	if s.dialect == dialectPostgres {
		query += ` FOR UPDATE`
	}
	state, err := scanTOTP(tx.QueryRow(s.q(query), email))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTOTPState
	}
	if err != nil {
		return err
	}
	err = action(tx, state)
	if err != nil && !errors.Is(err, ErrTOTPCode) {
		return err
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return commitErr
	}
	return err
}
func (s *Store) totpFailure(tx *sql.Tx, state *TOTPState) error {
	now := time.Now().Unix()
	failures := state.Failures + 1
	if state.LockedUntil != 0 && state.LockedUntil <= now {
		failures = 1
	}
	until := int64(0)
	if failures >= 5 {
		until = now + 300
	}
	if _, err := tx.Exec(s.q(`UPDATE account_totp SET failures=?,locked_until=? WHERE email=?`), failures, until, state.Email); err != nil {
		return err
	}
	if err := s.audit(tx, state.Email, "totp.verify.reject", state.Email); err != nil {
		return err
	}
	return ErrTOTPCode
}
func (s *Store) StartTOTPSetup(email, id, ciphertext string) error {
	return s.withTOTP(email, true, func(tx *sql.Tx, state *TOTPState) error {
		if state.Enabled() {
			return ErrTOTPState
		}
		if state.LockedUntil > time.Now().Unix() {
			return ErrTOTPLocked
		}
		if _, err := tx.Exec(s.q(`UPDATE account_totp SET setup_id=?,setup_secret=?,setup_expires=? WHERE email=?`), id, ciphertext, time.Now().Add(10*time.Minute).Unix(), state.Email); err != nil {
			return err
		}
		return s.audit(tx, state.Email, "totp.setup", state.Email)
	})
}
func (s *Store) ConfirmTOTPSetup(email, id string, hashes []string, check TOTPCheck) (version string, err error) {
	version = rand.Text()
	err = s.withTOTP(email, false, func(tx *sql.Tx, state *TOTPState) error {
		if state.Enabled() || state.SetupID != id || id == "" || state.SetupExpires <= time.Now().Unix() {
			return ErrTOTPState
		}
		if state.LockedUntil > time.Now().Unix() {
			return ErrTOTPLocked
		}
		step, err := check(state.SetupSecret, -1)
		if errors.Is(err, ErrTOTPCode) {
			return s.totpFailure(tx, state)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(s.q(`UPDATE account_totp SET secret=setup_secret,version=?,last_step=?,failures=0,locked_until=0,setup_id='',setup_secret='',setup_expires=0 WHERE email=?`), version, step, state.Email); err != nil {
			return err
		}
		if err := s.replaceTOTPRecovery(tx, state.Email, hashes); err != nil {
			return err
		}
		return s.audit(tx, state.Email, "totp.enable", state.Email)
	})
	return
}
func (s *Store) replaceTOTPRecovery(tx *sql.Tx, email string, hashes []string) error {
	if err := s.deleteTOTPRows(tx, "totp_recovery_codes", "code_hash", email, email, "totp.recovery.remove"); err != nil {
		return err
	}
	for _, hash := range hashes {
		if _, err := tx.Exec(s.q(`INSERT INTO totp_recovery_codes(email,code_hash) VALUES (?,?)`), email, hash); err != nil {
			return err
		}
		if err := s.audit(tx, email, "totp.recovery.create", email+":"+hash); err != nil {
			return err
		}
	}
	return nil
}

// Table and column identifiers are internal constants, never request input.
func (s *Store) deleteTOTPRows(tx *sql.Tx, table, column, email, actor, action string) error {
	rows, err := tx.Query(s.q("SELECT "+column+" FROM "+table+" WHERE email=?"), email)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.Exec(s.q("DELETE FROM "+table+" WHERE email=? AND "+column+"=?"), email, id); err != nil {
			return err
		}
		if err := s.audit(tx, actor, action, email+":"+id); err != nil {
			return err
		}
	}
	return nil
}

// verifyTOTPProof consumes either a recovery hash or a strictly increasing step.
func (s *Store) verifyTOTPProof(tx *sql.Tx, state *TOTPState, recoveryHash string, check TOTPCheck) error {
	if !state.Enabled() {
		return ErrTOTPState
	}
	if state.LockedUntil > time.Now().Unix() {
		return ErrTOTPLocked
	}
	step := state.LastStep
	if recoveryHash != "" {
		result, err := tx.Exec(s.q(`DELETE FROM totp_recovery_codes WHERE email=? AND code_hash=?`), state.Email, recoveryHash)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return s.totpFailure(tx, state)
		}
		if err := s.audit(tx, state.Email, "totp.recovery.consume", state.Email+":"+recoveryHash); err != nil {
			return err
		}
	} else {
		var err error
		step, err = check(state.Secret, state.LastStep)
		if errors.Is(err, ErrTOTPCode) {
			return s.totpFailure(tx, state)
		}
		if err != nil {
			return err
		}
		if step <= state.LastStep {
			return s.totpFailure(tx, state)
		}
	}
	if _, err := tx.Exec(s.q(`UPDATE account_totp SET last_step=?,failures=0,locked_until=0 WHERE email=?`), step, state.Email); err != nil {
		return err
	}
	return s.audit(tx, state.Email, "totp.verify", state.Email)
}
func (s *Store) ChangeTOTP(email, version, recoveryHash string, hashes []string, disable bool, check TOTPCheck) (newVersion string, err error) {
	newVersion = rand.Text()
	err = s.withTOTP(email, false, func(tx *sql.Tx, state *TOTPState) error {
		if state.Version != version {
			return ErrTOTPState
		}
		if err := s.verifyTOTPProof(tx, state, recoveryHash, check); err != nil {
			return err
		}
		if err := s.replaceTOTPRecovery(tx, state.Email, hashes); err != nil {
			return err
		}
		action := "totp.recovery.regenerate"
		if disable {
			action = "totp.disable"
			if _, err := tx.Exec(s.q(`UPDATE account_totp SET secret='',last_step=-1 WHERE email=?`), state.Email); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(s.q(`UPDATE account_totp SET version=? WHERE email=?`), newVersion, state.Email); err != nil {
			return err
		}
		return s.audit(tx, state.Email, action, state.Email)
	})
	return
}

func (s *Store) CreateTOTPChallenge(ch TOTPChallenge) error {
	return s.withTOTP(ch.Email, false, func(tx *sql.Tx, state *TOTPState) error {
		if !state.Enabled() || state.Version != ch.FactorVersion {
			return ErrTOTPState
		}
		if state.LockedUntil > time.Now().Unix() {
			return ErrTOTPLocked
		}
		if err := s.expireTOTPChallenges(tx); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(s.q(`SELECT COUNT(*) FROM totp_login_challenges WHERE email=? AND expires_at>?`), state.Email, time.Now().Unix()).Scan(&count); err != nil {
			return err
		}
		if count >= 8 {
			return ErrTOTPBusy
		}
		_, err := tx.Exec(s.q(`INSERT INTO totp_login_challenges(token_hash,email,user_id,password_version,factor_version,primary_at,expires_at) VALUES (?,?,?,?,?,?,?)`), ch.Hash, state.Email, ch.UserID, ch.PasswordVersion, ch.FactorVersion, ch.PrimaryAt, ch.ExpiresAt)
		if err != nil {
			return err
		}
		return s.audit(tx, state.Email, "totp.challenge.create", ch.Hash)
	})
}
func (s *Store) expireTOTPChallenges(tx *sql.Tx) error {
	rows, err := tx.Query(s.q(`SELECT token_hash,email FROM totp_login_challenges WHERE expires_at<=? ORDER BY token_hash LIMIT 100`), time.Now().Unix())
	if err != nil {
		return err
	}
	type expired struct{ hash, email string }
	var old []expired
	for rows.Next() {
		var item expired
		if err := rows.Scan(&item.hash, &item.email); err != nil {
			_ = rows.Close()
			return err
		}
		old = append(old, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range old {
		if _, err := tx.Exec(s.q(`DELETE FROM totp_login_challenges WHERE token_hash=?`), item.hash); err != nil {
			return err
		}
		if err := s.audit(tx, "system", "totp.challenge.expire", item.hash); err != nil {
			return err
		}
	}
	return nil
}
func scanTOTPChallenge(row interface{ Scan(...any) error }) (*TOTPChallenge, error) {
	var ch TOTPChallenge
	err := row.Scan(&ch.Hash, &ch.Email, &ch.UserID, &ch.PasswordVersion, &ch.FactorVersion, &ch.PrimaryAt, &ch.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTOTPState
	}
	return &ch, err
}

const challengeColumns = `token_hash,email,user_id,password_version,factor_version,primary_at,expires_at`

func (s *Store) TOTPChallenge(hash string) (*TOTPChallenge, error) {
	return scanTOTPChallenge(s.db.QueryRow(s.q(`SELECT `+challengeColumns+` FROM totp_login_challenges WHERE token_hash=?`), hash))
}
func (s *Store) ConsumeTOTPChallenge(hash, recoveryHash string, check TOTPCheck) (ch *TOTPChallenge, err error) {
	ch, err = s.TOTPChallenge(hash)
	if err != nil {
		return nil, err
	}
	err = s.withTOTP(ch.Email, false, func(tx *sql.Tx, state *TOTPState) error {
		query := `SELECT ` + challengeColumns + ` FROM totp_login_challenges WHERE token_hash=?`
		if s.dialect == dialectPostgres {
			query += ` FOR UPDATE`
		}
		current, err := scanTOTPChallenge(tx.QueryRow(s.q(query), hash))
		if err != nil {
			return err
		}
		if current.ExpiresAt <= time.Now().Unix() || current.FactorVersion != state.Version {
			return ErrTOTPState
		}
		// Password recovery invalidates the primary proof too, including races.
		if current.PasswordVersion != "" {
			var version string
			if err := tx.QueryRow(s.q(`SELECT version FROM account_passwords WHERE email=?`), state.Email).Scan(&version); err != nil {
				return err
			}
			if version != current.PasswordVersion {
				return ErrTOTPState
			}
		}
		if err := s.verifyTOTPProof(tx, state, recoveryHash, check); err != nil {
			return err
		}
		if _, err := tx.Exec(s.q(`DELETE FROM totp_login_challenges WHERE token_hash=?`), hash); err != nil {
			return err
		}
		return s.audit(tx, state.Email, "totp.challenge.consume", hash)
	})
	return
}
func (s *Store) ResetTOTP(email string) error {
	return s.withTOTP(email, false, func(tx *sql.Tx, state *TOTPState) error {
		if err := s.deleteTOTPRows(tx, "totp_recovery_codes", "code_hash", state.Email, "operator", "totp.recovery.remove"); err != nil {
			return err
		}
		if err := s.deleteTOTPRows(tx, "totp_login_challenges", "token_hash", state.Email, "operator", "totp.challenge.remove"); err != nil {
			return err
		}
		if _, err := tx.Exec(s.q(`UPDATE account_totp SET secret='',version=?,last_step=-1,failures=0,locked_until=0,setup_id='',setup_secret='',setup_expires=0 WHERE email=?`), rand.Text(), state.Email); err != nil {
			return err
		}
		return s.audit(tx, "operator", "totp.reset", state.Email)
	})
}
