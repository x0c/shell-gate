package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTOTPStorageContract(t *testing.T) {
	targets := map[string]string{"sqlite": filepath.Join(t.TempDir(), "totp.db")}
	if dsn := os.Getenv("SHELL_TEST_POSTGRES_URL"); dsn != "" {
		targets["postgres"] = dsn
	}
	for name, target := range targets {
		t.Run(name, func(t *testing.T) {
			st, err := Open(target)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = st.Close() }()
			email := fmt.Sprintf("totp-%d@example.com", time.Now().UnixNano())
			check := func(_ string, last int64) (int64, error) { return last + 1, nil }
			bad := func(_ string, _ int64) (int64, error) { return 0, ErrTOTPCode }
			state, err := st.TOTPState(email)
			if err != nil || state.Enabled() {
				t.Fatal("new account enabled", err)
			}
			if err = st.StartTOTPSetup(email, "setup", "encrypted-test-secret"); err != nil {
				t.Fatal(err)
			}
			if _, err = st.ConfirmTOTPSetup(email, "other", []string{"recovery1"}, check); !errors.Is(err, ErrTOTPState) {
				t.Fatal("wrong setup accepted", err)
			}
			version, err := st.ConfirmTOTPSetup(email, "setup", []string{"recovery1", "recovery2"}, check)
			if err != nil {
				t.Fatal(err)
			}
			state, err = st.TOTPState(email)
			if err != nil || !state.Enabled() || state.SetupSecret != "" || state.Version != version {
				t.Fatal("enable readback", err)
			}
			challenge := func(id string) error {
				return st.CreateTOTPChallenge(TOTPChallenge{Hash: email + id, Email: email, UserID: "test", FactorVersion: version, ExpiresAt: time.Now().Add(time.Minute).Unix()})
			}
			if err = challenge("a"); err != nil {
				t.Fatal(err)
			}
			if err = challenge("b"); err != nil {
				t.Fatal(err)
			}
			var successes atomic.Int32
			var wg sync.WaitGroup
			for _, id := range []string{"a", "b"} {
				wg.Go(func() {
					_, err := st.ConsumeTOTPChallenge(email+id, "recovery1", bad)
					if err == nil {
						successes.Add(1)
					} else if !errors.Is(err, ErrTOTPCode) {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			if successes.Load() != 1 {
				t.Fatal("recovery code was not single use", successes.Load())
			}
			if count, err := st.TOTPRecoveryCount(email); err != nil || count != 1 {
				t.Fatal("recovery readback", count, err)
			}
			// Attempts belong to the account and survive a new primary challenge.
			for range 4 {
				_, err = st.ChangeTOTP(email, version, "missing", nil, false, bad)
				if !errors.Is(err, ErrTOTPCode) {
					t.Fatal(err)
				}
			}
			if err = challenge("locked"); !errors.Is(err, ErrTOTPLocked) {
				t.Fatal("renewed challenge bypassed lock", err)
			}
			if err = st.ResetTOTP(email); err != nil {
				t.Fatal(err)
			}
			state, err = st.TOTPState(email)
			if err != nil || state.Enabled() || state.Version == version || state.Failures != 0 {
				t.Fatal("operator reset", err)
			}
			if count, err := st.TOTPRecoveryCount(email); err != nil || count != 0 {
				t.Fatal("reset retained recovery", err)
			}
			var count int
			if err = st.db.QueryRow(st.q(`SELECT COUNT(*) FROM audit_log WHERE actor='operator' AND action='totp.reset' AND target_id=?`), email).Scan(&count); err != nil || count != 1 {
				t.Fatal("missing operator audit", count, err)
			}
		})
	}
}

func TestTOTPRollbackExpiryAndReplay(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "totp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	const email = "user@example.com"
	good := func(_ string, _ int64) (int64, error) { return 42, nil }
	if err = st.StartTOTPSetup(email, "setup", "ciphertext"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.db.Exec(`UPDATE account_totp SET setup_expires=0`); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ConfirmTOTPSetup(email, "setup", nil, good); !errors.Is(err, ErrTOTPState) {
		t.Fatal("expired setup", err)
	}
	if err = st.StartTOTPSetup(email, "setup", "ciphertext"); err != nil {
		t.Fatal(err)
	}
	version, err := st.ConfirmTOTPSetup(email, "setup", []string{"unused"}, good)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.ChangeTOTP(email, version, "", nil, true, good); !errors.Is(err, ErrTOTPCode) {
		t.Fatal("replayed step", err)
	}
	if _, err = st.db.Exec(`CREATE TRIGGER reject_totp_audit BEFORE INSERT ON audit_log WHEN NEW.action='totp.disable' BEGIN SELECT RAISE(ABORT,'test audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ChangeTOTP(email, version, "unused", nil, true, good); err == nil {
		t.Fatal("mutation committed without audit")
	}
	state, err := st.TOTPState(email)
	if err != nil || !state.Enabled() || state.Version != version {
		t.Fatal("factor not rolled back", err)
	}
	if n, err := st.TOTPRecoveryCount(email); err != nil || n != 1 {
		t.Fatal("recovery consumption not rolled back", err)
	}
	ch := TOTPChallenge{Hash: "expired", Email: email, UserID: "test", FactorVersion: version, ExpiresAt: time.Now().Add(-time.Second).Unix()}
	if err = st.CreateTOTPChallenge(ch); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ConsumeTOTPChallenge(ch.Hash, "unused", good); !errors.Is(err, ErrTOTPState) {
		t.Fatal("expired challenge accepted", err)
	}
}
