package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestImportFromSQLiteIntoEmptyStore(t *testing.T) {
	srcPath := filepath.Join(t.TempDir(), "legacy.db")
	src, err := Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := src.InsertTab(Tab{
		ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Email: "operator@example.com",
		Machine: "linux", Title: "shell", Command: "", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Create("operator@example.com", "corral", "corral", "", 0); err != nil {
		t.Fatal(err)
	}
	_ = src.Close()

	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dst.Close() })
	imported, err := dst.ImportFromSQLite(srcPath)
	if err != nil || !imported {
		t.Fatalf("应导入: imported=%v err=%v", imported, err)
	}
	n, err := dst.CountTabs("operator@example.com")
	if err != nil || n != 1 {
		t.Fatalf("Tab 应导入: n=%d err=%v", n, err)
	}
	list, err := dst.List("operator@example.com")
	if err != nil || len(list) < 1 {
		t.Fatalf("预置应导入: n=%d err=%v", len(list), err)
	}
	importedAgain, err := dst.ImportFromSQLite(srcPath)
	if err != nil || importedAgain {
		t.Fatalf("非空库不得再导入: imported=%v err=%v", importedAgain, err)
	}
}
