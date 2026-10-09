package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestUpstreamWakerEnsureReady(t *testing.T) {
	tests := []struct {
		name          string
		probe         func(context.Context, string) error
		run           func(context.Context, string) error
		expectedRuns  int
		expectedError bool
	}{
		{
			name:  "上游已在线时不唤醒",
			probe: func(context.Context, string) error { return nil },
			run: func(context.Context, string) error {
				t.Fatal("在线上游不应执行唤醒命令")
				return nil
			},
		},
		{
			name: "离线上游执行一次唤醒后恢复",
			probe: func() func(context.Context, string) error {
				var mu sync.Mutex
				ready := false
				return func(context.Context, string) error {
					mu.Lock()
					defer mu.Unlock()
					if ready {
						return nil
					}
					return errors.New("offline")
				}
			}(),
			expectedRuns: 1,
		},
		{
			name:          "唤醒命令失败",
			probe:         func(context.Context, string) error { return errors.New("offline") },
			run:           func(context.Context, string) error { return errors.New("relay failed") },
			expectedRuns:  1,
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runs := 0
			waker := newUpstreamWaker("/wake", "127.0.0.1:7681")
			waker.probe = tt.probe
			if tt.run != nil {
				waker.run = func(ctx context.Context, command string) error {
					runs++
					return tt.run(ctx, command)
				}
			} else {
				waker.run = func(context.Context, string) error {
					runs++
					// 让「离线后恢复」用例在命令完成后立即变为在线。
					waker.probe = func(context.Context, string) error { return nil }
					return nil
				}
			}
			waker.readyTimeout = 10 * time.Millisecond
			waker.probeInterval = time.Millisecond

			_, err := waker.ensureReady(t.Context())
			if tt.expectedError && err == nil {
				t.Fatal("期望唤醒失败，实际成功")
			}
			if !tt.expectedError && err != nil {
				t.Fatalf("唤醒不应失败: %v", err)
			}
			if runs != tt.expectedRuns {
				t.Fatalf("唤醒命令执行 %d 次，期望 %d 次", runs, tt.expectedRuns)
			}
		})
	}
}

func TestUpstreamWakerTimesOutWaitingForReady(t *testing.T) {
	waker := newUpstreamWaker("/wake", "127.0.0.1:7681")
	waker.probe = func(context.Context, string) error { return errors.New("offline") }
	waker.run = func(context.Context, string) error { return nil }
	waker.readyTimeout = 5 * time.Millisecond
	waker.probeInterval = time.Millisecond

	if _, err := waker.ensureReady(t.Context()); err == nil {
		t.Fatal("上游一直离线时应超时")
	}
}

func TestUpstreamWakerCoalescesConcurrentRequests(t *testing.T) {
	var mu sync.Mutex
	ready := false
	runs := 0
	waker := newUpstreamWaker("/wake", "127.0.0.1:7681")
	waker.probe = func(context.Context, string) error {
		mu.Lock()
		defer mu.Unlock()
		if ready {
			return nil
		}
		return errors.New("offline")
	}
	waker.run = func(context.Context, string) error {
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		runs++
		ready = true
		return nil
	}
	waker.readyTimeout = 20 * time.Millisecond
	waker.probeInterval = time.Millisecond

	const callers = 6
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := waker.ensureReady(t.Context())
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("并发唤醒不应失败: %v", err)
		}
	}
	if runs != 1 {
		t.Fatalf("并发请求应合并为一次唤醒，实际执行 %d 次", runs)
	}
}

type stubHomePresence struct {
	atHome bool
	err    error
	calls  int
}

func (s *stubHomePresence) ComfortHome(context.Context) (bool, error) {
	s.calls++
	return s.atHome, s.err
}

func TestUpstreamWakerSuppressesWhenAtHome(t *testing.T) {
	home := &stubHomePresence{atHome: true}
	waker := newUpstreamWaker("/wake", "127.0.0.1:7681")
	waker.homeCheck = home
	waker.probe = func(context.Context, string) error { return errors.New("offline") }
	waker.run = func(context.Context, string) error {
		t.Fatal("在家时不应执行唤醒命令")
		return nil
	}

	_, err := waker.ensureReady(t.Context())
	if !errors.Is(err, ErrWakeSuppressedAtHome) {
		t.Fatalf("期望 ErrWakeSuppressedAtHome，得到 %v", err)
	}
	if home.calls != 1 {
		t.Fatalf("应查询一次在家状态，实际 %d", home.calls)
	}
}

func TestUpstreamWakerWakesWhenAway(t *testing.T) {
	home := &stubHomePresence{atHome: false}
	waker := newUpstreamWaker("/wake", "127.0.0.1:7681")
	waker.homeCheck = home
	waker.probe = func(context.Context, string) error { return errors.New("offline") }
	runs := 0
	waker.run = func(context.Context, string) error {
		runs++
		waker.probe = func(context.Context, string) error { return nil }
		return nil
	}

	if _, err := waker.ensureReady(t.Context()); err != nil {
		t.Fatalf("不在家应允许唤醒: %v", err)
	}
	if runs != 1 || home.calls != 1 {
		t.Fatalf("runs=%d home.calls=%d", runs, home.calls)
	}
}

func TestUpstreamWakerFailOpenOnPresenceError(t *testing.T) {
	home := &stubHomePresence{err: errors.New("harbor down")}
	waker := newUpstreamWaker("/wake", "127.0.0.1:7681")
	waker.homeCheck = home
	waker.probe = func(context.Context, string) error { return errors.New("offline") }
	runs := 0
	waker.run = func(context.Context, string) error {
		runs++
		waker.probe = func(context.Context, string) error { return nil }
		return nil
	}

	if _, err := waker.ensureReady(t.Context()); err != nil {
		t.Fatalf("presence 失败应 fail-open: %v", err)
	}
	if runs != 1 {
		t.Fatalf("应仍执行唤醒，实际 %d", runs)
	}
}

func TestUpstreamWakerSkipsHomeCheckWhenAlreadyOnline(t *testing.T) {
	home := &stubHomePresence{atHome: true}
	waker := newUpstreamWaker("/wake", "127.0.0.1:7681")
	waker.homeCheck = home
	waker.probe = func(context.Context, string) error { return nil }
	waker.run = func(context.Context, string) error {
		t.Fatal("在线不应唤醒")
		return nil
	}

	if _, err := waker.ensureReady(t.Context()); err != nil {
		t.Fatal(err)
	}
	if home.calls != 0 {
		t.Fatalf("已在线不应查在家，实际 %d", home.calls)
	}
}
