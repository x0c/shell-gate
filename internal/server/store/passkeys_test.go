package store

import (
	"github.com/go-webauthn/webauthn/webauthn"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// An isolated PostgreSQL database can run the same behavioral contract.
func TestPasskeyStorageContract(t *testing.T) {
	targets := map[string]string{"sqlite": filepath.Join(t.TempDir(), "keys.db")}
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
			email := "key-" + time.Now().Format("150405.000000000") + "@example.com"
			token, err := st.Invite(email, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = st.EnrollmentUser("other@example.com", token); err == nil {
				t.Fatal("invitation crossed accounts")
			}
			u, err := st.EnrollmentUser(email, token)
			if err != nil {
				t.Fatal(err)
			}
			cred := &webauthn.Credential{ID: []byte(email), PublicKey: []byte("storage-contract-public-key")}
			if err = st.Enroll(u, token, cred); err != nil {
				t.Fatal(err)
			}
			if err = st.Enroll(u, token, cred); err == nil {
				t.Fatal("reused invitation")
			}
			got, err := st.PasskeyUserByHandle(u.ID)
			if err != nil || got.Email != email || len(got.Credentials) != 1 {
				t.Fatalf("readback: %v %v", got, err)
			}
			if err = st.UpdatePasskey(email, cred); err != nil {
				t.Fatal(err)
			}
			if err = st.UpdatePasskey("other@example.com", cred); err == nil {
				t.Fatal("key changed across accounts")
			}
			var count int
			if err = st.db.QueryRow(st.q(`SELECT COUNT(*) FROM audit_log WHERE actor = ?`), email).Scan(&count); err != nil || count != 4 {
				t.Fatalf("audit records: %d %v", count, err)
			}
		})
	}
}

func TestAuditFailureRollsBackMutation(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err = st.db.Exec(`DROP TABLE audit_log`); err != nil {
		t.Fatal(err)
	}
	if _, err = st.Create("user@example.com", "Shell", "echo hello", "", 0); err == nil {
		t.Fatal("mutation succeeded without audit")
	}
	var count int
	if err = st.db.QueryRow(`SELECT COUNT(*) FROM presets`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback: %d %v", count, err)
	}
}

func TestSQLiteSidecarsHavePrivatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission contract")
	}
	path := filepath.Join(t.TempDir(), "private.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err = st.Invite("user@example.com", time.Hour); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s permissions: %o", suffix, info.Mode().Perm())
		}
	}
}

func TestPasswordStorageContract(t *testing.T) {
	targets := map[string]string{"sqlite": filepath.Join(t.TempDir(), "password.db")}
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
			email := "password-" + time.Now().Format("150405.000000000") + "@example.com"
			token, err := st.Invite(email, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			u, err := st.EnrollmentUser(email, token)
			if err != nil {
				t.Fatal(err)
			}
			if err = st.EnrollPassword(u, token, "test-only-hash"); err != nil {
				t.Fatal(err)
			}
			hash, version, err := st.PasswordState(u.Email)
			if err != nil || hash != "test-only-hash" || version == "" {
				t.Fatal("credential not stored")
			}
			if err = st.EnrollPassword(u, token, "other-hash"); err == nil {
				t.Fatal("reused invitation")
			}
			token, err = st.Invite(u.Email, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if err = st.EnrollPassword(u, token, "recovered-test-hash"); err != nil {
				t.Fatal(err)
			}
			hash, newVersion, err := st.PasswordState(u.Email)
			if err != nil || hash != "recovered-test-hash" || newVersion == version {
				t.Fatal("recovery did not invalidate previous sessions")
			}
			var count int
			if err = st.db.QueryRow(st.q(`SELECT COUNT(*) FROM audit_log WHERE actor = ? AND action = 'account.password.set'`), email).Scan(&count); err != nil || count != 2 {
				t.Fatalf("password audit: %d %v", count, err)
			}
		})
	}
}

func TestPasswordAuditFailureRollsBackEnrollment(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "password-rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	token, err := st.Invite("user@example.com", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.EnrollmentUser("user@example.com", token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.db.Exec(`DROP TABLE audit_log`); err != nil {
		t.Fatal(err)
	}
	if err = st.EnrollPassword(u, token, "test-hash"); err == nil {
		t.Fatal("credential changed without audit")
	}
	if hash, _, err := st.PasswordState(u.Email); err != nil || hash != "" {
		t.Fatalf("password rollback: %v", err)
	}
	if _, err = st.EnrollmentUser(u.Email, token); err != nil {
		t.Fatal("invitation was consumed despite rollback")
	}
}
