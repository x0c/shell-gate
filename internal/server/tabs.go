package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/x0c/shell-gate/internal/server/store"
)

// TabSession 是一次已打开的终端 Tab（目录按账号落库；明确关闭才删除）。
type TabSession struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Machine   string    `json:"machine"` // 目标上游机器名（开在哪个终端主机上）
	Title     string    `json:"title"`
	Command   string    `json:"command"` // 空表示登录 shell
	CreatedAt time.Time `json:"createdAt"`
}

// tabStore 以共享数据库为唯一真相（双门禁可见同一套 Tab）。
type tabStore struct {
	db TabRepository
}

func newTabStore(db TabRepository) (*tabStore, error) {
	if db == nil {
		return nil, fmt.Errorf("tab storage is required")
	}
	return &tabStore{db: db}, nil
}

func (t *tabStore) Create(email, machine, title, command string) (*TabSession, error) {
	id := newSessionID()
	s := &TabSession{
		ID:        id,
		Email:     strings.ToLower(strings.TrimSpace(email)),
		Machine:   machine,
		Title:     title,
		Command:   command,
		CreatedAt: time.Now().UTC(),
	}
	if err := t.db.InsertTab(store.Tab{
		ID:        s.ID,
		Email:     s.Email,
		Machine:   s.Machine,
		Title:     s.Title,
		Command:   s.Command,
		CreatedAt: s.CreatedAt,
	}); err != nil {
		return nil, err
	}
	return s, nil
}

func (t *tabStore) Get(id string) (*TabSession, bool) {
	row, err := t.db.GetTab(id)
	if err != nil {
		log.Printf("读取 Tab 失败 id=%s: %v", id, err)
		return nil, false
	}
	if row == nil {
		return nil, false
	}
	return tabFromRow(*row), true
}

// All 返回当前全部 Tab 快照（启动时补申请 Mac 租约用）。
func (t *tabStore) All() ([]TabSession, error) {
	rows, err := t.db.ListAllTabs()
	if err != nil {
		log.Printf("列出全部 Tab 失败: %v", err)
		return nil, err
	}
	out := make([]TabSession, 0, len(rows))
	for _, row := range rows {
		out = append(out, *tabFromRow(row))
	}
	return out, nil
}

// ListFor 返回同一登录账号仍存活的 Tab。工作台按此全量挂载，绝不暗中新建。
func (t *tabStore) ListFor(email string) ([]TabSession, error) {
	rows, err := t.db.ListTabs(email)
	if err != nil {
		log.Printf("列出账号 Tab 失败: %v", err)
		return nil, err
	}
	out := make([]TabSession, 0, len(rows))
	for _, row := range rows {
		out = append(out, *tabFromRow(row))
	}
	return out, nil
}

// CountFor 统计某个账号当前开着的 Tab 数（监控面板用）。
func (t *tabStore) CountFor(email string) (int, error) {
	n, err := t.db.CountTabs(email)
	if err != nil {
		log.Printf("统计 Tab 失败: %v", err)
		return 0, err
	}
	return n, nil
}

func (t *tabStore) Delete(email, id string) (bool, error) {
	deleted, err := t.db.DeleteTab(email, id)
	if err != nil {
		log.Printf("删除持久化 Tab 失败 id=%s: %v", id, err)
		return false, err
	}
	return deleted, nil
}

func tabFromRow(row store.Tab) *TabSession {
	return &TabSession{
		ID:        row.ID,
		Email:     row.Email,
		Machine:   row.Machine,
		Title:     row.Title,
		Command:   row.Command,
		CreatedAt: row.CreatedAt,
	}
}

func newSessionID() string {
	h := sha256.Sum256([]byte(rand.Text()))
	return hex.EncodeToString(h[:16])
}
