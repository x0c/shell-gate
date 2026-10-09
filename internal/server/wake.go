package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os/exec"
	"sync"
	"time"
)

const (
	wakeCommandTimeout = 25 * time.Second
	wakeReadyTimeout   = 50 * time.Second
	wakeProbeTimeout   = time.Second
	wakeProbeInterval  = time.Second
)

// ErrWakeSuppressedAtHome 表示家里判定用户在家，禁止发网络唤醒。
var ErrWakeSuppressedAtHome = errors.New("在家时已关闭自动唤醒")

type wakeLocker interface {
	WithWakeLock(ctx context.Context, machine string, fn func() error) error
}

type upstreamWaker struct {
	command        string
	target         string
	machine        string
	locker         wakeLocker
	homeCheck      homePresence // 非 nil 时，离线唤醒前查「舒适在家」
	commandTimeout time.Duration
	readyTimeout   time.Duration
	probeInterval  time.Duration
	run            func(context.Context, string) error
	probe          func(context.Context, string) error
	mu             sync.Mutex // 单测未注入 locker 时的进程内锁
}

func newUpstreamWaker(command, target string) *upstreamWaker {
	return &upstreamWaker{
		command:        command,
		target:         target,
		commandTimeout: wakeCommandTimeout,
		readyTimeout:   wakeReadyTimeout,
		probeInterval:  wakeProbeInterval,
		run:            runWakeCommand,
		probe:          probeTCP,
	}
}

func newUpstreamWakerLocked(command, target, machine string, locker wakeLocker, homeCheck homePresence) *upstreamWaker {
	w := newUpstreamWaker(command, target)
	w.machine = machine
	w.locker = locker
	w.homeCheck = homeCheck
	return w
}

// ensureReady 只在明确的用户连接动作中调用。后台健康探测与隐藏 Tab 重连不得调用，
// 否则 Mac 会在用户不用它时也被反复叫醒。跨门禁用库锁合并唤醒，避免重复发包。
func (w *upstreamWaker) ensureReady(ctx context.Context) (bool, error) {
	if err := w.probe(ctx, w.target); err == nil {
		return false, nil
	}

	var woke bool
	run := func() error {
		if err := w.probe(ctx, w.target); err == nil {
			woke = false
			return nil
		}
		if err := w.rejectIfAtHome(ctx); err != nil {
			return err
		}
		commandCtx, cancelCommand := context.WithTimeout(ctx, w.commandTimeout)
		err := w.run(commandCtx, w.command)
		cancelCommand()
		if err != nil {
			return fmt.Errorf("执行唤醒命令: %w", err)
		}
		readyCtx, cancelReady := context.WithTimeout(ctx, w.readyTimeout)
		defer cancelReady()
		ticker := time.NewTicker(w.probeInterval)
		defer ticker.Stop()
		for {
			if err := w.probe(readyCtx, w.target); err == nil {
				woke = true
				return nil
			}
			select {
			case <-readyCtx.Done():
				return fmt.Errorf("等待上游恢复: %w", readyCtx.Err())
			case <-ticker.C:
			}
		}
	}

	if w.locker != nil && w.machine != "" {
		if err := w.locker.WithWakeLock(ctx, w.machine, run); err != nil {
			return false, err
		}
		return woke, nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if err := run(); err != nil {
		return false, err
	}
	return woke, nil
}

// rejectIfAtHome：舒适在家则拒绝唤醒；查询失败 fail-open（打日志后放行）。
func (w *upstreamWaker) rejectIfAtHome(ctx context.Context) error {
	if w.homeCheck == nil {
		return nil
	}
	atHome, err := w.homeCheck.ComfortHome(ctx)
	if err != nil {
		log.Printf("在家检测失败，继续尝试唤醒 machine=%s: %v", w.machine, err)
		return nil
	}
	if atHome {
		return ErrWakeSuppressedAtHome
	}
	return nil
}

func runWakeCommand(ctx context.Context, command string) error {
	if err := exec.CommandContext(ctx, command).Run(); err != nil {
		return fmt.Errorf("%s: %w", command, err)
	}
	return nil
}

func wakeFailureMessage(machine string, err error) string {
	if errors.Is(err, ErrWakeSuppressedAtHome) {
		return machine + " 当前判定为在家，已关闭自动唤醒；请本机唤醒或离家后再连"
	}
	return machine + " 未能自动唤醒；请确认机器已接电且未关机"
}

func probeTCP(ctx context.Context, target string) error {
	dialer := net.Dialer{Timeout: wakeProbeTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return err
	}
	return conn.Close()
}
