package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// mutate commits the user mutation and its audit record together. No content or
// credentials are copied into the audit trail.
func (s *Store) mutate(actor, action, target, query string, args ...any) (int64, error) {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(s.q(query), args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n > 0 {
		if err := s.audit(tx, actor, action, target); err != nil {
			return 0, err
		}
	}
	return n, tx.Commit()
}

func (s *Store) audit(tx *sql.Tx, actor, action, target string) error {
	if actor == "" || action == "" || target == "" {
		return fmt.Errorf("audit identity is incomplete")
	}
	_, err := tx.Exec(s.q(`INSERT INTO audit_log (actor, action, target_id, occurred_at) VALUES (?, ?, ?, ?)`),
		actor, action, target, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) migrateAudit() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS audit_log (
  actor TEXT NOT NULL, action TEXT NOT NULL, target_id TEXT NOT NULL, occurred_at TEXT NOT NULL
)`)
	return err
}
