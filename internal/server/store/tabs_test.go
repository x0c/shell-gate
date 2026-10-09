package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestTabsPersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tabs.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 7, 6, 0, 0, 0, time.UTC)
	if err := st.InsertTab(Tab{
		ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Email: "User@Example.COM",
		Machine: "linux", Title: "shell", Command: "", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	got, err := st2.GetTab("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil || got == nil {
		t.Fatalf("应读回 Tab: err=%v got=%v", err, got)
	}
	if got.Email != "user@example.com" || got.Machine != "linux" || got.Title != "shell" {
		t.Fatalf("字段不对: %#v", got)
	}
	listed, err := st2.ListTabs("USER@example.com")
	if err != nil || len(listed) != 1 {
		t.Fatalf("按邮箱列出失败: err=%v n=%d", err, len(listed))
	}
	ok, err := st2.DeleteTab("user@example.com", got.ID)
	if err != nil || !ok {
		t.Fatalf("删除失败: ok=%v err=%v", ok, err)
	}
	n, err := st2.CountTabs("user@example.com")
	if err != nil || n != 0 {
		t.Fatalf("删除后计数应为 0: n=%d err=%v", n, err)
	}
}
