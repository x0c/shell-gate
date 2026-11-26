package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const testAgentToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestRegistryAuthAndMethodValidation(t *testing.T) {
	reg := NewRegistry(map[string]string{"mac": testAgentToken})
	srv := httptest.NewServer(http.HandlerFunc(reg.HandleUpgrade))
	defer srv.Close()
	defer reg.Close()

	// POST rejected
	req, _ := http.NewRequest(http.MethodPost, srv.URL+TunnelPath+"?name=mac", nil)
	req.Header.Set("Authorization", "Bearer "+testAgentToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}

	// Missing / wrong token rejected with 401
	for _, hdr := range []string{"", "Basic abc", "Bearer wrong"} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+TunnelPath+"?name=mac", nil)
		if hdr != "" {
			req.Header.Set("Authorization", hdr)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("hdr=%q expected 401, got %d", hdr, resp.StatusCode)
		}
	}

	// Unknown agent rejected
	req, _ = http.NewRequest(http.MethodGet, srv.URL+TunnelPath+"?name=other", nil)
	req.Header.Set("Authorization", "Bearer "+testAgentToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unknown agent, got %d", resp.StatusCode)
	}
}

func TestAgentMultiplexedHTTPWebSocketAndControlHooks(t *testing.T) {
	// Local backend serving HTTP and WebSocket echo
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
			if err != nil {
				return
			}
			defer func() { _ = c.CloseNow() }()
			for {
				typ, msg, err := c.Read(r.Context())
				if err != nil {
					return
				}
				if err := c.Write(r.Context(), typ, append([]byte("echo:"), msg...)); err != nil {
					return
				}
			}
		}
		_, _ = io.WriteString(w, "hello-from-"+r.URL.Path)
	}))
	defer backend.Close()

	// Local presence server
	presenceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"comfort_home": true})
	}))
	defer presenceSrv.Close()

	// Executable wake and lease test hooks
	tmpDir := t.TempDir()
	hookLog := filepath.Join(tmpDir, "hooks.log")
	wakeScript := filepath.Join(tmpDir, "wake.sh")
	leaseScript := filepath.Join(tmpDir, "lease.sh")
	writeScript(t, wakeScript, fmt.Sprintf("#!/bin/sh\necho wake >> %q\n", hookLog))
	writeScript(t, leaseScript, fmt.Sprintf("#!/bin/sh\necho \"lease:$1:$2\" >> %q\n", hookLog))

	reg := NewRegistry(map[string]string{"mac": testAgentToken})
	defer reg.Close()
	gw := httptest.NewServer(http.HandlerFunc(reg.HandleUpgrade))
	defer gw.Close()

	agent, err := NewAgent(AgentConfig{
		Name:         "mac",
		Gateways:     []string{gw.URL},
		Token:        testAgentToken,
		TTYDAddr:     backend.URL,
		PresenceAddr: presenceSrv.URL,
		WakeHooks:    map[string]string{"mac": wakeScript},
		LeaseHooks:   map[string]string{"mac": leaseScript},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- agent.Run(ctx) }()

	waitFor(t, 5*time.Second, func() bool { return reg.HasAgent("mac") })

	// 1. ReverseProxy HTTP + WebSocket over tunnel
	targetURL, _ := url.Parse("http://mac.shellgate.internal")
	rp := httputil.NewSingleHostReverseProxy(targetURL)
	rp.Transport = &http.Transport{
		Proxy: nil,
		DialContext: func(dctx context.Context, _, _ string) (net.Conn, error) {
			return reg.DialService(dctx, "mac", ServiceTTYD)
		},
		ForceAttemptHTTP2: false,
	}
	front := httptest.NewServer(rp)
	defer front.Close()

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resp, err := http.Get(fmt.Sprintf("%s/p/%d", front.URL, idx))
			if err != nil {
				t.Errorf("http get %d: %v", idx, err)
				return
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if string(body) != fmt.Sprintf("hello-from-/p/%d", idx) {
				t.Errorf("unexpected body: %q", string(body))
			}
		}(i)
	}
	wg.Wait()

	// WebSocket echo through reverse proxy -> tunnel -> backend
	wsFront := "ws" + strings.TrimPrefix(front.URL, "http") + "/ws"
	c, resp, err := websocket.Dial(ctx, wsFront, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err := c.Write(ctx, websocket.MessageText, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	_, got, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "echo:ping" {
		t.Fatalf("expected echo:ping, got %q", string(got))
	}
	_ = c.Close(websocket.StatusNormalClosure, "ok")

	// 2. Presence HTTP dial over tunnel
	pClient := &http.Client{
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(dctx context.Context, _, _ string) (net.Conn, error) {
				return reg.DialService(dctx, "mac", ServicePresence)
			},
		},
	}
	presp, err := pClient.Get("http://presence.shellgate.internal/v1/presence")
	if err != nil {
		t.Fatal(err)
	}
	var pbody struct {
		ComfortHome bool `json:"comfort_home"`
	}
	_ = json.NewDecoder(presp.Body).Decode(&pbody)
	_ = presp.Body.Close()
	if !pbody.ComfortHome {
		t.Fatalf("expected comfort_home=true")
	}

	// 3. Reject unknown service (SSRF protection)
	if _, err := reg.DialService(ctx, "mac", "ssh"); err == nil {
		t.Fatalf("expected error dialing unknown service")
	}

	// 4. Wake and Lease hooks
	if err := reg.InvokeWake(ctx, "mac", "mac"); err != nil {
		t.Fatalf("InvokeWake: %v", err)
	}
	tabID := "0123456789abcdef0123456789abcdef"
	if err := reg.InvokeLease(ctx, "mac", "mac", "acquire", tabID); err != nil {
		t.Fatalf("InvokeLease acquire: %v", err)
	}
	if err := reg.InvokeLease(ctx, "mac", "mac", "release", tabID); err != nil {
		t.Fatalf("InvokeLease release: %v", err)
	}
	if err := reg.InvokeLease(ctx, "mac", "mac", "acquire", "not-hex"); err == nil {
		t.Fatalf("expected invalid tabId error")
	}

	rawLog, err := os.ReadFile(hookLog)
	if err != nil {
		t.Fatal(err)
	}
	want := "wake\nlease:acquire:" + tabID + "\nlease:release:" + tabID + "\n"
	if string(rawLog) != want {
		t.Fatalf("hook log=%q want=%q", string(rawLog), want)
	}

	// 5. Session replacement & reconnect: closing registry drops agent, then agent reconnects
	reg.Close()
	cancel()
	if err := <-done; err != nil && err != context.Canceled {
		t.Fatalf("unexpected agent exit: %v", err)
	}
}

func TestAgentReconnectsAfterSessionReset(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer backend.Close()

	reg := NewRegistry(map[string]string{"suzhou": testAgentToken})
	defer reg.Close()
	gw := httptest.NewServer(http.HandlerFunc(reg.HandleUpgrade))
	defer gw.Close()

	agent, err := NewAgent(AgentConfig{
		Name:     "suzhou",
		Gateways: []string{gw.URL},
		Token:    testAgentToken,
		TTYDAddr: backend.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = agent.Run(ctx) }()

	waitFor(t, 5*time.Second, func() bool { return reg.HasAgent("suzhou") })
	agent.resetSessions()
	waitFor(t, 5*time.Second, func() bool {
		conn, err := reg.DialService(ctx, "suzhou", ServiceTTYD)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	})
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}
