//go:build darwin

package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMacLeaseRefreshAndReleaseAreSerialized(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "mac", "shell-gate-session-lease"))
	if err != nil {
		t.Fatal(err)
	}
	// Exercise real caffeinate/assertion/lockf behavior across a shortened expiry.
	script := strings.ReplaceAll(string(raw), "readonly lease_seconds=28800", "readonly lease_seconds=4")
	script = strings.ReplaceAll(script, "readonly lease_refresh_seconds=1800", "readonly lease_refresh_seconds=1")
	dir := t.TempDir()
	path := filepath.Join(dir, "lease")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	const id = "0123456789abcdef0123456789abcdef"
	run := func(action string) error {
		cmd := exec.Command("/bin/bash", path, action, id)
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+dir)
		out, runErr := cmd.CombinedOutput()
		if runErr != nil {
			return &leaseScriptError{action: action, output: string(out), err: runErr}
		}
		return nil
	}
	t.Cleanup(func() { _ = run("release") })
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if err := run("acquire"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	state := filepath.Join(dir, "shell-gate", "leases", id)
	readPID := func() string {
		t.Helper()
		b, readErr := os.ReadFile(state)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return strings.Fields(string(b))[0]
	}
	oldPID := readPID()
	time.Sleep(1100 * time.Millisecond)
	if err := run("renew"); err != nil {
		t.Fatal(err)
	}
	newPID := readPID()
	if newPID == oldPID {
		t.Fatal("renewal did not replace the aging assertion")
	}
	time.Sleep(3100 * time.Millisecond)
	out, err := exec.Command("/bin/ps", "-p", newPID, "-o", "command=").Output()
	if err != nil || !strings.Contains(string(out), "/usr/bin/caffeinate -is -t 4") {
		t.Fatalf("renewed protection did not survive the original expiry: %q %v", out, err)
	}
	for _, action := range []string{"renew", "release", "renew", "renew"} {
		wg.Go(func() {
			if err := run(action); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := run("renew"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("late renewal resurrected a released lease: %v", err)
	}
}

type leaseScriptError struct {
	action, output string
	err            error
}

func (e *leaseScriptError) Error() string { return e.action + ": " + e.err.Error() + ": " + e.output }
