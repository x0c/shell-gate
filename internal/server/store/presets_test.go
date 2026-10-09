package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// 旧库（多上游支持之前）没有 machine 列，Open 时必须补列且默认值可用；
// 新建的库直接带列。两条路径都要保证 machine 能存能取。
func TestPresetMachineMigrationAndRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// 1) 全新库：machine 默认空串，绑定后能原样读回
	s, err := Open(filepath.Join(dir, "new.db"))
	if err != nil {
		t.Fatalf("打开新库失败: %v", err)
	}
	p, err := s.Create("operator@example.com", "corral", "corral", "linux", 0)
	if err != nil {
		t.Fatalf("新建绑定机器的预置失败: %v", err)
	}
	got, err := s.Get("operator@example.com", p.ID)
	if err != nil || got == nil {
		t.Fatalf("读取预置失败: %v %v", got, err)
	}
	if got.Machine != "linux" {
		t.Fatalf("机器绑定应原样读回，得到 %q", got.Machine)
	}
	// 更新成跟随选择（空串）
	if _, err := s.Update("operator@example.com", p.ID, "corral", "corral", "", 0); err != nil {
		t.Fatalf("更新预置失败: %v", err)
	}
	got, _ = s.Get("operator@example.com", p.ID)
	if got.Machine != "" {
		t.Fatalf("清空机器绑定失败，得到 %q", got.Machine)
	}
	_ = s.Close()

	// 2) 旧库：手工建出「没有 machine 列」的历史表结构并塞一条数据，
	// Open 的迁移必须补列且不丢既有行。
	legacy := filepath.Join(dir, "legacy.db")
	raw, err := sql.Open("sqlite", legacy)
	if err != nil {
		t.Fatalf("打开旧库失败: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE presets (
		id TEXT PRIMARY KEY, email TEXT NOT NULL, name TEXT NOT NULL, command TEXT NOT NULL,
		sort_order INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("建旧结构表失败: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO presets (id, email, name, command, sort_order, created_at, updated_at)
		VALUES ('p1', 'operator@example.com', 'corral', 'corral', 0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("塞旧数据失败: %v", err)
	}
	_ = raw.Close()

	s3, err := Open(legacy)
	if err != nil {
		t.Fatalf("迁移旧库失败: %v", err)
	}
	list, err := s3.List("operator@example.com")
	if err != nil {
		t.Fatalf("读迁移后的预置失败: %v", err)
	}
	var legacyRow *Preset
	for i := range list {
		if list[i].ID == "p1" {
			legacyRow = &list[i]
		}
	}
	if legacyRow == nil {
		t.Fatalf("迁移丢了旧行: %+v", list)
	}
	if legacyRow.Machine != "" {
		t.Fatalf("旧数据迁移后机器绑定应为空串（跟随选择），得到 %q", legacyRow.Machine)
	}
	_ = s3.Close()
}

// 删掉默认预置后再次 List，不得把同名命令补种回来——否则管理面板点「删」像没反应。
func TestDeleteDefaultPresetDoesNotReseed(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "presets.db"))
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	defer func() { _ = s.Close() }()

	s.Defaults = []DefaultPreset{{Name: "corral", Command: "corral"}, {Name: "subswap", Command: "subswap"}}
	email := "operator@example.com"
	first, err := s.List(email)
	if err != nil {
		t.Fatalf("首次列表失败: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("新账号应种下 2 个默认预置，得到 %d", len(first))
	}

	var subswapID string
	for _, p := range first {
		if p.Command == "subswap" {
			subswapID = p.ID
		}
	}
	if subswapID == "" {
		t.Fatalf("未找到 subswap 默认预置: %+v", first)
	}
	ok, err := s.Delete(email, subswapID)
	if err != nil || !ok {
		t.Fatalf("删除 subswap 失败: ok=%v err=%v", ok, err)
	}

	second, err := s.List(email)
	if err != nil {
		t.Fatalf("删除后再次列表失败: %v", err)
	}
	for _, p := range second {
		if p.Command == "subswap" {
			t.Fatalf("删除后不应再出现 subswap，得到 %+v", second)
		}
	}
	if len(second) != 1 || second[0].Command != "corral" {
		t.Fatalf("删除后应只剩 corral，得到 %+v", second)
	}
}
