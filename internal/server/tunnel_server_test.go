package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	authkit "github.com/x0c/shell-gate/internal/auth"
	"github.com/x0c/shell-gate/internal/tunnel"
)

const testTunnelSecret = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

func TestParseAgentTunnelConfigValidation(t *testing.T) {
	t.Setenv("SHELL_PUBLIC_URL", "https://shell.example.com")
	t.Setenv("SHELL_ALLOWED_EMAILS", "owner@example.com")
	t.Setenv("SHELL_DATA_DIR", t.TempDir())
	t.Setenv("SHELL_TTYD_TARGETS", "mac=agent://mac")
	t.Setenv("SHELL_AGENT_TOKENS", "mac="+testTunnelSecret)
	t.Setenv("SHELL_WAKE_AGENT_ROUTES", "mac=mac")
	t.Setenv("SHELL_LEASE_AGENT_ROUTES", "mac=mac")
	t.Setenv("SHELL_WAKE_HOME_PRESENCE_URL", "http://127.0.0.1:18780/v1/presence")
	t.Setenv("SHELL_WAKE_HOME_PRESENCE_TOKEN", "presence-tok")
	t.Setenv("SHELL_WAKE_HOME_PRESENCE_MACHINES", "mac")
	t.Setenv("SHELL_WAKE_HOME_PRESENCE_AGENT", "mac")

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv valid: %v", err)
	}
	if cfg.Machines[0].URL.Scheme != "agent" || cfg.Machines[0].URL.Host != "mac" {
		t.Fatalf("unexpected URL: %v", cfg.Machines[0].URL)
	}
	if cfg.Machines[0].WakeAgent != "mac" || cfg.Machines[0].LeaseAgent != "mac" {
		t.Fatalf("unexpected agent routes: %+v", cfg.Machines[0])
	}
	if cfg.WakeHomePresence.Agent != "mac" {
		t.Fatalf("unexpected presence agent: %+v", cfg.WakeHomePresence)
	}

	// Short token rejected
	t.Setenv("SHELL_AGENT_TOKENS", "mac=too-short")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("expected short agent token error")
	}

	// agent:// referencing unknown token rejected
	t.Setenv("SHELL_AGENT_TOKENS", "other="+testTunnelSecret)
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("expected unknown agent target error")
	}

	// Bad agent:// URL rejected
	t.Setenv("SHELL_TTYD_TARGETS", "mac=agent://mac:7681/path")
	t.Setenv("SHELL_AGENT_TOKENS", "mac="+testTunnelSecret)
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("expected invalid agent:// URL error")
	}
}

func TestServerEndToEndAgentTunnel(t *testing.T) {
	// Fake local ttyd backend serving HTML and compressed/echo WS
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(authHeaderName) == "" {
			http.Error(w, "missing auth header", http.StatusProxyAuthRequired)
			return
		}
		if r.URL.Path == "/ws" && strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
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
				if err := c.Write(r.Context(), typ, append([]byte("ttyd:"), msg...)); err != nil {
					return
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html><body>terminal</body></html>")
	}))
	defer backend.Close()

	// Presence service reports away (comfort_home: false)
	presenceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"comfort_home": false})
	}))
	defer presenceSrv.Close()

	// Local executable wake & lease hooks on agent
	tmpDir := t.TempDir()
	hookLog := filepath.Join(tmpDir, "hooks.log")
	wakeScript := filepath.Join(tmpDir, "wake.sh")
	leaseScript := filepath.Join(tmpDir, "lease.sh")
	if err := os.WriteFile(wakeScript, []byte(fmt.Sprintf("#!/bin/sh\necho wake >> %q\n", hookLog)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(leaseScript, []byte(fmt.Sprintf("#!/bin/sh\necho \"lease:$1:$2\" >> %q\n", hookLog)), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SHELL_PUBLIC_URL", "")
	t.Setenv("SHELL_LISTEN", "127.0.0.1:0")
	t.Setenv("SHELL_AUTH_MODE", "local")
	t.Setenv("SHELL_ALLOWED_EMAILS", "owner@example.com")
	t.Setenv("SHELL_DATA_DIR", filepath.Join(tmpDir, "data"))
	t.Setenv("SHELL_NETMON_INTERVAL", "0s")
	t.Setenv("SHELL_TTYD_TARGETS", "mac=agent://mac")
	t.Setenv("SHELL_AGENT_TOKENS", "mac="+testTunnelSecret)
	t.Setenv("SHELL_WAKE_AGENT_ROUTES", "mac=mac")
	t.Setenv("SHELL_LEASE_AGENT_ROUTES", "mac=mac")
	t.Setenv("SHELL_WAKE_HOME_PRESENCE_URL", "http://127.0.0.1:1/v1/presence")
	t.Setenv("SHELL_WAKE_HOME_PRESENCE_TOKEN", "presence-tok")
	t.Setenv("SHELL_WAKE_HOME_PRESENCE_MACHINES", "mac")
	t.Setenv("SHELL_WAKE_HOME_PRESENCE_AGENT", "mac")

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	// Configure PublicURL later only if needed; for httptest.NewServer host match, use local cfg directly.
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Shutdown() }()

	front := httptest.NewUnstartedServer(srv.httpSrv.Handler)
	srv.cfg.PublicURL = "http://" + front.Listener.Addr().String()
	front.Start()
	defer front.Close()

	// 1. Agent tunnel connects without Origin header (exempt from browser CSRF)
	agent, err := tunnel.NewAgent(tunnel.AgentConfig{
		Name:         "mac",
		Gateways:     []string{front.URL},
		Token:        testTunnelSecret,
		TTYDAddr:     backend.URL,
		PresenceAddr: presenceSrv.URL,
		WakeHooks:    map[string]string{"mac": wakeScript},
		LeaseHooks:   map[string]string{"mac": leaseScript},
	})
	if err != nil {
		t.Fatal(err)
	}
	agentCtx, agentCancel := context.WithCancel(context.Background())
	defer agentCancel()
	go func() { _ = agent.Run(agentCtx) }()

	deadline := time.Now().Add(5 * time.Second)
	for !srv.tunnels.HasAgent("mac") && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if !srv.tunnels.HasAgent("mac") {
		t.Fatal("agent failed to register tunnel")
	}

	// Mint session cookie for API requests
	token, err := srv.mintSessionWithFactors(&authkit.Identity{UserID: "u1", Email: "owner@example.com"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: srv.cfg.CookieName, Value: token}

	// 2. /api/machines reports transport=tunnel
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/api/machines", nil)
	req.AddCookie(cookie)
	mresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var machinesOut struct {
		Machines []machineView `json:"machines"`
	}
	_ = json.NewDecoder(mresp.Body).Decode(&machinesOut)
	_ = mresp.Body.Close()
	if len(machinesOut.Machines) != 1 || machinesOut.Machines[0].Transport != "tunnel" {
		t.Fatalf("unexpected machines: %+v", machinesOut.Machines)
	}

	// 3. Create Mac tab -> runs wake check (online) + lease acquire over tunnel
	req, _ = http.NewRequest(http.MethodPost, front.URL+"/api/tabs", strings.NewReader(`{"machine":"mac","title":"t"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	cresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var tabOut struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	}
	_ = json.NewDecoder(cresp.Body).Decode(&tabOut)
	_ = cresp.Body.Close()
	if cresp.StatusCode != http.StatusOK || tabOut.ID == "" {
		t.Fatalf("create tab status=%d body=%+v", cresp.StatusCode, tabOut)
	}

	// 4. Proxy terminal HTML with injected enhance.js
	req, _ = http.NewRequest(http.MethodGet, front.URL+tabOut.Path, nil)
	req.AddCookie(cookie)
	hresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	htmlBody, _ := io.ReadAll(hresp.Body)
	_ = hresp.Body.Close()
	if hresp.StatusCode != http.StatusOK || !strings.Contains(string(htmlBody), enhanceScriptTag) {
		t.Fatalf("unexpected HTML response status=%d body=%q", hresp.StatusCode, string(htmlBody))
	}

	// 5. Proxy terminal WebSocket through tunnel
	wsURL := "ws" + strings.TrimPrefix(front.URL, "http") + tabOut.Path + "ws"
	wsHeaders := http.Header{
		"Cookie": {cookie.String()},
		"Origin": {front.URL},
	}
	wsc, wresp, err := websocket.Dial(agentCtx, wsURL, &websocket.DialOptions{HTTPHeader: wsHeaders})
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	if wresp != nil && wresp.Body != nil {
		_ = wresp.Body.Close()
	}
	if err := wsc.Write(agentCtx, websocket.MessageText, []byte("ls")); err != nil {
		t.Fatal(err)
	}
	_, echo, err := wsc.Read(agentCtx)
	if err != nil {
		t.Fatal(err)
	}
	if string(echo) != "ttyd:ls" {
		t.Fatalf("unexpected ws echo: %q", string(echo))
	}
	_ = wsc.Close(websocket.StatusNormalClosure, "done")

	// 6. Delete tab -> releases lease over tunnel
	req, _ = http.NewRequest(http.MethodDelete, front.URL+"/api/tabs/"+tabOut.ID, nil)
	req.AddCookie(cookie)
	dresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = dresp.Body.Close()
	if dresp.StatusCode != http.StatusOK {
		t.Fatalf("delete tab status=%d", dresp.StatusCode)
	}

	// 7. Invoke wake directly over tunnel via waker.customRun
	if err := srv.upstreamByName["mac"].waker.customRun(agentCtx); err != nil {
		t.Fatalf("wake via tunnel: %v", err)
	}

	rawHooks, err := os.ReadFile(hookLog)
	if err != nil {
		t.Fatal(err)
	}
	gotHooks := string(rawHooks)
	if !strings.Contains(gotHooks, "lease:acquire:"+tabOut.ID) || !strings.Contains(gotHooks, "lease:release:"+tabOut.ID) || !strings.Contains(gotHooks, "wake") {
		t.Fatalf("unexpected hook log:\n%s", gotHooks)
	}
}
