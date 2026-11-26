package server

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"sync"
	"time"
)

const sessionLeaseCommandTimeout = 12 * time.Second

// sessionLeaser executes a fixed hook with separate action and tab arguments.
// Connections renew protection; disconnection never releases the remote lease.
type sessionLeaser struct {
	command       string
	run           func(context.Context, string, string, string) error
	customRun     func(context.Context, string, string) error
	renewInterval time.Duration
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	closed        bool
	wg            sync.WaitGroup
}

func newSessionLeaser(command string) *sessionLeaser {
	ctx, cancel := context.WithCancel(context.Background())
	return &sessionLeaser{command: command, run: runSessionLeaseCommand, renewInterval: 15 * time.Minute, ctx: ctx, cancel: cancel}
}

// keepAlive renews only an established terminal connection. Stopping renewal
// preserves the remote lease's expiry; explicit tab deletion owns release.
func (l *sessionLeaser) keepAlive(parent context.Context, tabID string) func() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return func() {}
	}
	ctx, cancel := context.WithCancel(parent)
	stopShutdown := context.AfterFunc(l.ctx, cancel)
	done := make(chan struct{})
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		defer close(done)
		defer cancel()
		defer stopShutdown()
		ticker := time.NewTicker(l.renewInterval)
		defer ticker.Stop()
		for ctx.Err() == nil {
			if err := l.runWithTimeout(ctx, "renew", tabID); err != nil && ctx.Err() == nil {
				log.Printf("Session lease renewal failed tab=%s: %v", tabID, err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}

func (l *sessionLeaser) shutdown() {
	l.mu.Lock()
	l.closed = true
	l.cancel()
	l.mu.Unlock()
	l.wg.Wait()
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
	var err error
	if l.customRun != nil {
		err = l.customRun(ctx, action, tabID)
	} else {
		err = l.run(ctx, l.command, action, tabID)
	}
	if err != nil {
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
