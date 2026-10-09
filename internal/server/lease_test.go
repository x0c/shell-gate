package server

import (
	"context"
	"errors"
	"testing"
)

func TestSessionLeaserPassesFixedActionAndTabID(t *testing.T) {
	leaser := newSessionLeaser("/lease")
	var gotCommand, gotAction, gotID string
	leaser.run = func(_ context.Context, command, action, id string) error {
		gotCommand, gotAction, gotID = command, action, id
		return nil
	}
	const tabID = "0123456789abcdef0123456789abcdef"
	if err := leaser.acquire(context.Background(), tabID); err != nil {
		t.Fatalf("创建租约失败: %v", err)
	}
	if gotCommand != "/lease" || gotAction != "acquire" || gotID != tabID {
		t.Fatalf("租约参数不正确: command=%q action=%q id=%q", gotCommand, gotAction, gotID)
	}
	if err := leaser.release(context.Background(), tabID); err != nil {
		t.Fatalf("释放租约失败: %v", err)
	}
	if gotAction != "release" {
		t.Fatalf("释放动作未透传: %q", gotAction)
	}
}

func TestSessionLeaserRejectsInvalidTabIDWithoutExecuting(t *testing.T) {
	leaser := newSessionLeaser("/lease")
	called := false
	leaser.run = func(context.Context, string, string, string) error {
		called = true
		return errors.New("不应执行")
	}
	if err := leaser.acquire(context.Background(), "../not-a-tab"); err == nil {
		t.Fatal("非法 Tab 标识必须失败")
	}
	if called {
		t.Fatal("非法 Tab 标识不应触发外部命令")
	}
}

func TestValidSessionLeaseID(t *testing.T) {
	if !validSessionLeaseID("0123456789abcdef0123456789abcdef") {
		t.Fatal("32 位小写十六进制标识应合法")
	}
	for _, id := range []string{"", "0123456789abcdef", "0123456789abcdef0123456789abcdeg", "0123456789ABCDEF0123456789ABCDEF"} {
		if validSessionLeaseID(id) {
			t.Fatalf("非法标识被接受: %q", id)
		}
	}
}
