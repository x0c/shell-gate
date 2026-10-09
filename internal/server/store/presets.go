package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type dialect int

const (
	dialectSQLite dialect = iota
	dialectPostgres
)

// Preset 是用户可配置的「新开 Tab 默认程序」。
type Preset struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Command   string    `json:"command"`
	Machine   string    `json:"machine"` // 绑定的上游机器名；空 = 跟随当前选择
	SortOrder int       `json:"sortOrder"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// DefaultPreset is an operator-configured launcher seeded once per account.
type DefaultPreset struct {
	Name    string `json:"name"`
	Command string `json:"command"`
}

type Store struct {
	db       *sql.DB
	dialect  dialect
	Defaults []DefaultPreset
	wakeMu   sync.Mutex // SQLite / 单测：进程内唤醒锁
}

// Open selects PostgreSQL by DSN scheme, or local SQLite by file path.
func Open(dsnOrPath string) (*Store, error) {
	if isPostgresDSN(dsnOrPath) {
		return openPostgres(dsnOrPath)
	}
	return openSQLite(dsnOrPath)
}

func isPostgresDSN(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(lower, "postgres://") ||
		strings.HasPrefix(lower, "postgresql://") ||
		strings.HasPrefix(lower, "pgx://")
}

func openPostgres(dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("could not open PostgreSQL; check connection settings")
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)
	s := &Store{db: db, dialect: dialectPostgres}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("could not connect to PostgreSQL; check connection settings and network")
	}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func openSQLite(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}
	// SQLite derives journal permissions from the database file. Restrict it
	// before enabling WAL, including when an older database already exists.
	if path != ":memory:" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, err
		}
		if err := errors.Join(f.Chmod(0o600), f.Close()); err != nil {
			return nil, err
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			if err := os.Chmod(path+suffix, 0o600); err != nil && !os.IsNotExist(err) {
				return nil, err
			}
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开 sqlite 失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if path != ":memory:" {
		if err := os.Chmod(path, 0o600); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	s := &Store{db: db, dialect: dialectSQLite}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Ping 探测库是否可达（健康检查用）。
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("存储未初始化")
	}
	return s.db.PingContext(ctx)
}

// DialectPostgres 报告当前是否为 Postgres（部署与迁移日志用）。
func (s *Store) DialectPostgres() bool {
	return s != nil && s.dialect == dialectPostgres
}

func (s *Store) q(query string) string {
	if s.dialect != dialectPostgres {
		return query
	}
	var b strings.Builder
	n := 0
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(query[i])
	}
	return b.String()
}

func (s *Store) migrate() error {
	if err := s.migrateAudit(); err != nil {
		return err
	}
	if err := s.migratePasskeys(); err != nil {
		return err
	}
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS presets (
  id TEXT PRIMARY KEY,
  email TEXT NOT NULL,
  name TEXT NOT NULL,
  command TEXT NOT NULL,
  machine TEXT NOT NULL DEFAULT '',
  sort_order INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
)`)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_presets_email ON presets(email, sort_order, name)`); err != nil {
		return err
	}
	_, err = s.db.Exec(`
CREATE TABLE IF NOT EXISTS preset_defaults_applied (
  email TEXT PRIMARY KEY,
  applied_at TEXT NOT NULL
)`)
	if err != nil {
		return err
	}
	if err := s.migrateTabs(); err != nil {
		return err
	}
	if s.dialect == dialectSQLite {
		hasMachine, err := s.sqliteColumnExists("presets", "machine")
		if err != nil {
			return err
		}
		if !hasMachine {
			if _, err := s.db.Exec(`ALTER TABLE presets ADD COLUMN machine TEXT NOT NULL DEFAULT ''`); err != nil {
				return err
			}
		}
	}
	return s.markExistingDefaults()

}

func (s *Store) sqliteColumnExists(table, column string) (bool, error) {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("关闭表结构查询: %w", closeErr))
		}
	}()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// WithWakeLock 在双门禁场景下串行化某台上游的唤醒；Postgres 用 advisory lock。
func (s *Store) WithWakeLock(ctx context.Context, machine string, fn func() error) error {
	if s == nil {
		return fn()
	}
	if s.dialect != dialectPostgres {
		s.wakeMu.Lock()
		defer s.wakeMu.Unlock()
		return fn()
	}
	key := wakeLockKey(machine)
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("取连接做唤醒锁失败: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, key); err != nil {
		return fmt.Errorf("获取唤醒锁失败: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, key)
	}()
	return fn()
}

func wakeLockKey(machine string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("shell-gate-wake:" + machine))
	return int64(h.Sum64() & 0x7fffffffffffffff)
}

// EnsureDefaults 只在该邮箱首次进入预置列表时写入默认项。
func (s *Store) EnsureDefaults(email string) error {
	email = normalizeEmail(email)
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := tx.Exec(s.q(`INSERT INTO preset_defaults_applied (email, applied_at) VALUES (?, ?) ON CONFLICT (email) DO NOTHING`), email, now)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return tx.Commit()
	}
	if err := s.audit(tx, email, "preset.defaults", email); err != nil {
		return err
	}
	for i, d := range s.Defaults {
		id := "preset_" + randID()
		if _, err := tx.Exec(s.q(`INSERT INTO presets (id, email, name, command, sort_order, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`), id, email, d.Name, d.Command, i, now, now); err != nil {
			return err
		}
		if err := s.audit(tx, email, "preset.create", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// List 列出某邮箱的预置，按排序。
func (s *Store) List(email string) (out []Preset, err error) {
	email = normalizeEmail(email)
	if err := s.EnsureDefaults(email); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(s.q(
		`SELECT id, email, name, command, machine, sort_order, created_at, updated_at
		 FROM presets WHERE email = ? ORDER BY sort_order ASC, name ASC`),
		email,
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("关闭预置查询结果: %w", closeErr))
		}
	}()
	out = make([]Preset, 0)
	for rows.Next() {
		var p Preset
		var created, updated string
		if err := rows.Scan(&p.ID, &p.Email, &p.Name, &p.Command, &p.Machine, &p.SortOrder, &created, &updated); err != nil {
			return nil, err
		}
		p.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, p)
	}
	return out, rows.Err()
}

// Get 按 id 取预置，且必须属于该邮箱。
func (s *Store) Get(email, id string) (*Preset, error) {
	email = normalizeEmail(email)
	var p Preset
	var created, updated string
	err := s.db.QueryRow(s.q(
		`SELECT id, email, name, command, machine, sort_order, created_at, updated_at
		 FROM presets WHERE id = ? AND email = ?`),
		id, email,
	).Scan(&p.ID, &p.Email, &p.Name, &p.Command, &p.Machine, &p.SortOrder, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return &p, nil
}

// Create 新建预置。
func (s *Store) Create(email, name, command, machine string, sortOrder int) (*Preset, error) {
	email = normalizeEmail(email)
	name = strings.TrimSpace(name)
	command = strings.TrimSpace(command)
	machine = strings.TrimSpace(machine)
	if name == "" || command == "" {
		return nil, fmt.Errorf("名称与命令不能为空")
	}
	now := time.Now().UTC()
	p := &Preset{
		ID:        "preset_" + randID(),
		Email:     email,
		Name:      name,
		Command:   command,
		Machine:   machine,
		SortOrder: sortOrder,
		CreatedAt: now,
		UpdatedAt: now,
	}
	_, err := s.mutate(email, "preset.create", p.ID,
		`INSERT INTO presets (id, email, name, command, machine, sort_order, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Email, p.Name, p.Command, p.Machine, p.SortOrder,
		p.CreatedAt.Format(time.RFC3339Nano), p.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// Update 更新预置。
func (s *Store) Update(email, id, name, command, machine string, sortOrder int) (*Preset, error) {
	email = normalizeEmail(email)
	name = strings.TrimSpace(name)
	command = strings.TrimSpace(command)
	machine = strings.TrimSpace(machine)
	if name == "" || command == "" {
		return nil, fmt.Errorf("名称与命令不能为空")
	}
	now := time.Now().UTC()
	n, err := s.mutate(email, "preset.update", id,
		`UPDATE presets SET name = ?, command = ?, machine = ?, sort_order = ?, updated_at = ?
		 WHERE id = ? AND email = ?`,
		name, command, machine, sortOrder, now.Format(time.RFC3339Nano), id, email,
	)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	return s.Get(email, id)
}

// Delete 删除预置。
func (s *Store) Delete(email, id string) (bool, error) {
	email = normalizeEmail(email)
	n, err := s.mutate(email, "preset.delete", id, `DELETE FROM presets WHERE id = ? AND email = ?`, id, email)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// EmptyDurable 判断生产库是否还没有任何 Tab/预置（用于一次性导入旧 SQLite）。
func (s *Store) EmptyDurable() (bool, error) {
	var presets, tabs int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM presets`).Scan(&presets); err != nil {
		return false, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM tabs`).Scan(&tabs); err != nil {
		return false, err
	}
	return presets == 0 && tabs == 0, nil
}

// ImportFromSQLite 把旧 shell-gate.db 导入当前（通常是 Postgres）库；目标非空则跳过。
func (s *Store) ImportFromSQLite(sqlitePath string) (imported bool, err error) {
	empty, err := s.EmptyDurable()
	if err != nil || !empty {
		return false, err
	}
	if _, err := os.Stat(sqlitePath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	src, err := Open(sqlitePath)
	if err != nil {
		return false, fmt.Errorf("打开旧 sqlite 失败: %w", err)
	}
	defer func() { _ = src.Close() }()
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if s.dialect == dialectPostgres {
		if _, err := tx.Exec(`LOCK TABLE presets, tabs IN EXCLUSIVE MODE`); err != nil {
			return false, err
		}
	}
	var present int
	if err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM presets) + (SELECT COUNT(*) FROM tabs)`).Scan(&present); err != nil {
		return false, err
	}
	if present != 0 {
		return false, nil
	}

	presets, err := src.db.Query(`SELECT id, email, name, command, machine, sort_order, created_at, updated_at FROM presets`)
	if err != nil {
		return false, err
	}
	defer func() { _ = presets.Close() }()
	for presets.Next() {
		var p Preset
		var created, updated string
		if err := presets.Scan(&p.ID, &p.Email, &p.Name, &p.Command, &p.Machine, &p.SortOrder, &created, &updated); err != nil {
			return false, err
		}
		if _, err := tx.Exec(s.q(
			`INSERT INTO presets (id, email, name, command, machine, sort_order, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
			p.ID, p.Email, p.Name, p.Command, p.Machine, p.SortOrder, created, updated,
		); err != nil {
			return false, err
		}
		if err := s.audit(tx, "system:import", "preset.import", p.ID); err != nil {
			return false, err
		}
	}
	if err := presets.Err(); err != nil {
		return false, err
	}

	defaults, err := src.db.Query(`SELECT email, applied_at FROM preset_defaults_applied`)
	if err != nil {
		return false, err
	}
	defer func() { _ = defaults.Close() }()
	for defaults.Next() {
		var email, applied string
		if err := defaults.Scan(&email, &applied); err != nil {
			return false, err
		}
		if s.dialect == dialectPostgres {
			_, err = tx.Exec(s.q(
				`INSERT INTO preset_defaults_applied (email, applied_at) VALUES (?, ?)
				 ON CONFLICT (email) DO NOTHING`), email, applied)
		} else {
			_, err = tx.Exec(
				`INSERT OR IGNORE INTO preset_defaults_applied (email, applied_at) VALUES (?, ?)`, email, applied)
		}
		if err != nil {
			return false, err
		}
		if err := s.audit(tx, "system:import", "preset.defaults.import", email); err != nil {
			return false, err
		}
	}
	if err := defaults.Err(); err != nil {
		return false, err
	}

	tabs, err := src.ListAllTabs()
	if err != nil {
		return false, err
	}
	for _, tab := range tabs {
		if _, err := tx.Exec(s.q(`INSERT INTO tabs (id,email,machine,title,command,created_at) VALUES (?,?,?,?,?,?)`), tab.ID, tab.Email, tab.Machine, tab.Title, tab.Command, tab.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return false, err
		}
		if err := s.audit(tx, "system:import", "tab.import", tab.ID); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func randID() string { return rand.Text() }

func (s *Store) markExistingDefaults() error {
	rows, err := s.db.Query(`SELECT DISTINCT email FROM presets WHERE email NOT IN (SELECT email FROM preset_defaults_applied)`)
	if err != nil {
		return err
	}
	var emails []string
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			_ = rows.Close()
			return err
		}
		emails = append(emails, email)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, email := range emails {
		if _, err := s.mutate("system:migration", "preset.defaults.migrate", email, `INSERT INTO preset_defaults_applied (email,applied_at) VALUES (?,?) ON CONFLICT (email) DO NOTHING`, email, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return nil
}
