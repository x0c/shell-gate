package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestTerminalLeaseRenewsOnlyAfterSuccessfulUpgrade(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("reject") != "" || r.URL.Path != "/ws" {
			http.Error(w, "not upgraded", http.StatusBadGateway)
			return
		}
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		_ = rw.Flush()
		_, _ = io.Copy(conn, conn)
	}))
	defer backend.Close()
	s := newTestServer(t)
	u := s.primaryUpstream()
	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.proxy = newTTYDProxy(target, u.netmon)
	u.leaser = newSessionLeaser("/lease")
	u.leaser.renewInterval = 10 * time.Millisecond
	t.Cleanup(u.leaser.shutdown)
	called := make(chan string, 128)
	u.leaser.run = func(_ context.Context, _, action, _ string) error {
		called <- action
		return nil
	}
	tab := mustCreateTab(t, s.tabs, "operator@example.com", "default", "lease test", "")
	finished := make(chan struct{}, 8)
	gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handleTermProxy(w, r, "operator@example.com")
		finished <- struct{}{}
	}))
	defer gate.Close()
	for _, path := range []string{"/t/" + tab.ID + "/", "/t/" + tab.ID + "/ws?reject=1", "/t/not-owned/ws"} {
		req, reqErr := http.NewRequest(http.MethodGet, gate.URL+path, nil)
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		resp, reqErr := http.DefaultClient.Do(req)
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		_ = resp.Body.Close()
		<-finished
		select {
		case action := <-called:
			t.Fatalf("unsuccessful/unowned request renewed a lease: %s", action)
		default:
		}
	}
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(gate.URL, "http://"), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = fmt.Fprintf(conn, "GET /t/%s/ws HTTP/1.1\r\nHost: example.test\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n", tab.ID)
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade failed: response=%v error=%v", resp, err)
	}
	_, _ = conn.Write([]byte("terminal round trip\n"))
	line, err := reader.ReadString('\n')
	if err != nil || line != "terminal round trip\n" {
		t.Fatalf("tunnel I/O failed: %q, %v", line, err)
	}
	for range 3 {
		select {
		case action := <-called:
			if action != "renew" {
				t.Fatalf("unexpected connected lease action: %s", action)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("long-running tunnel did not renew")
		}
	}
	_ = conn.Close()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("renewal worker did not stop when tunnel closed")
	}
	for len(called) > 0 {
		if action := <-called; action != "renew" {
			t.Fatalf("disconnect must preserve the lease, got %s", action)
		}
	}
}
