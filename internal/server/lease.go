package server

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

const sessionLeaseCommandTimeout = 12 * time.Second

// sessionLeaser 只执行配置好的固定入口；动作与随机 Tab ID 始终作为独立参数传递。
// 租约独立于 ttyd WebSocket，因此浏览器后台、断线或刷新不会意外释放 Mac 的睡眠断言。
type sessionLeaser struct {
	command string
	run     func(context.Context, string, string, string) error
}

func newSessionLeaser(command string) *sessionLeaser {
	return &sessionLeaser{command: command, run: runSessionLeaseCommand}
}

func (l *sessionLeaser) acquire(ctx context.Context, tabID string) error {
	return l.runWithTimeout(ctx, "acquire", tabID)
}

func (l *sessionLeaser) release(ctx context.Context, tabID string) error {
	return l.runWithTimeout(ctx, "release", tabID)
}

func (l *sessionLeaser) runWithTimeout(parent context.Context, action, tabID string) error {
	if !validSessionLeaseID(tabID) {
		return fmt.Errorf("非法 Tab 标识")
	}
	ctx, cancel := context.WithTimeout(parent, sessionLeaseCommandTimeout)
	defer cancel()
	if err := l.run(ctx, l.command, action, tabID); err != nil {
		return fmt.Errorf("%s 租约: %w", action, err)
	}
	return nil
}

func runSessionLeaseCommand(ctx context.Context, command, action, tabID string) error {
	if err := exec.CommandContext(ctx, command, action, tabID).Run(); err != nil {
		return fmt.Errorf("%s: %w", command, err)
	}
	return nil
}

func validSessionLeaseID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
