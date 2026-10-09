package server

import (
	"path/filepath"
	"testing"

	"github.com/x0c/shell-gate/internal/server/store"
)

func testTabDB(t *testing.T) *tabStore {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "tabs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return mustTabStore(t, st)
}

func TestTabStoreDeleteIgnoresEmailCase(t *testing.T) {
	tabs := testTabDB(t)
	tab := mustCreateTab(t, tabs, "User@Example.COM", "linux", "shell", "")
	if deleted, err := tabs.Delete("user@example.com", tab.ID); !deleted || err != nil {
		t.Fatal("期望大小写不同的邮箱仍能删除 Tab")
	}
	if _, ok := tabs.Get(tab.ID); ok {
		t.Fatal("删除后 Tab 仍存在")
	}
}

func TestTabStoreDeleteRejectsOtherEmail(t *testing.T) {
	tabs := testTabDB(t)
	tab := mustCreateTab(t, tabs, "owner@example.com", "linux", "shell", "")
	if deleted, err := tabs.Delete("other@example.com", tab.ID); deleted || err != nil {
		t.Fatal("不应允许他人邮箱删除 Tab")
	}
	if _, ok := tabs.Get(tab.ID); !ok {
		t.Fatal("误删了不属于请求者的 Tab")
	}
}

func TestTabStoreListForKeepsOnlyOwnersTabsInCreationOrder(t *testing.T) {
	tabs := testTabDB(t)
	first := mustCreateTab(t, tabs, "operator@example.com", "mac", "first", "")
	mustCreateTab(t, tabs, "other@example.com", "mac", "other", "")
	second := mustCreateTab(t, tabs, "OPERATOR@EXAMPLE.COM", "linux", "second", "")
	got, err := tabs.ListFor("operator@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != first.ID || got[1].ID != second.ID {
		t.Fatalf("列表必须只返回当前账号并保持创建顺序: %#v", got)
	}
}
