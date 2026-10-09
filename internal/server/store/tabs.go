package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Tab 是按账号持久保存的工作台终端页签。
type Tab struct {
	ID        string
	Email     string
	Machine   string
	Title     string
	Command   string
	CreatedAt time.Time
}

func (s *Store) migrateTabs() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS tabs (
  id TEXT PRIMARY KEY,
  email TEXT NOT NULL,
  machine TEXT NOT NULL,
  title TEXT NOT NULL,
  command TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
)`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_tabs_email_created ON tabs(email, created_at)`)
	return err
}

// InsertTab 写入一条 Tab；id 由调用方生成。
func (s *Store) InsertTab(tab Tab) error {
	tab.Email = normalizeEmail(tab.Email)
	tab.Machine = strings.TrimSpace(tab.Machine)
	tab.Title = strings.TrimSpace(tab.Title)
	tab.Command = strings.TrimSpace(tab.Command)
	if tab.ID == "" || tab.Email == "" || tab.Machine == "" || tab.Title == "" {
		return fmt.Errorf("Tab 字段不完整")
	}
	if tab.CreatedAt.IsZero() {
		tab.CreatedAt = time.Now().UTC()
	}
	_, err := s.mutate(tab.Email, "tab.create", tab.ID,
		`INSERT INTO tabs (id, email, machine, title, command, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		tab.ID, tab.Email, tab.Machine, tab.Title, tab.Command,
		tab.CreatedAt.Format(time.RFC3339Nano),
	)
	return err
}

// GetTab 按 id 取 Tab（不校验邮箱；调用方自行核对归属）。
func (s *Store) GetTab(id string) (*Tab, error) {
	var tab Tab
	var created string
	err := s.db.QueryRow(s.q(
		`SELECT id, email, machine, title, command, created_at FROM tabs WHERE id = ?`),
		id,
	).Scan(&tab.ID, &tab.Email, &tab.Machine, &tab.Title, &tab.Command, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tab.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return &tab, nil
}

// ListTabs 列出某邮箱的全部 Tab，按创建时间升序。
func (s *Store) ListTabs(email string) ([]Tab, error) {
	email = normalizeEmail(email)
	rows, err := s.db.Query(s.q(
		`SELECT id, email, machine, title, command, created_at
		 FROM tabs WHERE email = ? ORDER BY created_at ASC`),
		email,
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("关闭 Tab 查询结果: %w", closeErr))
		}
	}()
	out := make([]Tab, 0)
	for rows.Next() {
		var tab Tab
		var created string
		if err := rows.Scan(&tab.ID, &tab.Email, &tab.Machine, &tab.Title, &tab.Command, &created); err != nil {
			return nil, err
		}
		tab.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, tab)
	}
	return out, rows.Err()
}

// ListAllTabs 返回库中全部 Tab（门禁启动时补申请 Mac 租约用）。
func (s *Store) ListAllTabs() ([]Tab, error) {
	rows, err := s.db.Query(
		`SELECT id, email, machine, title, command, created_at FROM tabs ORDER BY created_at ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("关闭全部 Tab 查询结果: %w", closeErr))
		}
	}()
	out := make([]Tab, 0)
	for rows.Next() {
		var tab Tab
		var created string
		if err := rows.Scan(&tab.ID, &tab.Email, &tab.Machine, &tab.Title, &tab.Command, &created); err != nil {
			return nil, err
		}
		tab.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, tab)
	}
	return out, rows.Err()
}

// CountTabs 统计某邮箱当前 Tab 数。
func (s *Store) CountTabs(email string) (int, error) {
	email = normalizeEmail(email)
	var n int
	err := s.db.QueryRow(s.q(`SELECT COUNT(1) FROM tabs WHERE email = ?`), email).Scan(&n)
	return n, err
}

// DeleteTab 删除属于该邮箱的 Tab。
func (s *Store) DeleteTab(email, id string) (bool, error) {
	email = normalizeEmail(email)
	n, err := s.mutate(email, "tab.delete", id, `DELETE FROM tabs WHERE id = ? AND email = ?`, id, email)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
