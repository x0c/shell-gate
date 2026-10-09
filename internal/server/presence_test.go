package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHarborPresenceClientComfortHome(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    any
		want    bool
		wantErr bool
	}{
		{name: "在家", status: 200, body: map[string]any{"comfort_home": true, "network_home": false}, want: true},
		{name: "不在家", status: 200, body: map[string]any{"comfort_home": false}, want: false},
		{name: "非 200", status: 401, body: map[string]string{"message": "no"}, wantErr: true},
		{name: "非法 JSON", status: 200, body: "not-json", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-token" {
					t.Fatalf("缺少 Bearer: %q", r.Header.Get("Authorization"))
				}
				w.WriteHeader(tt.status)
				switch v := tt.body.(type) {
				case string:
					_, _ = w.Write([]byte(v))
				default:
					_ = json.NewEncoder(w).Encode(v)
				}
			}))
			t.Cleanup(srv.Close)

			client := newHarborPresenceClient(srv.URL, "test-token")
			got, err := client.ComfortHome(t.Context())
			if tt.wantErr {
				if err == nil {
					t.Fatal("期望错误")
				}
				return
			}
			if err != nil {
				t.Fatalf("不应失败: %v", err)
			}
			if got != tt.want {
				t.Fatalf("comfort_home=%v，期望 %v", got, tt.want)
			}
		})
	}
}

func TestHarborPresenceClientRejectsRedirect(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"comfort_home": true})
	}))
	t.Cleanup(final.Close)
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	t.Cleanup(redir.Close)

	client := newHarborPresenceClient(redir.URL, "tok")
	_, err := client.ComfortHome(context.Background())
	if err == nil {
		t.Fatal("跟随重定向应失败")
	}
}

func TestParseWakeHomePresence(t *testing.T) {
	machines := []MachineConfig{{Name: "linux"}, {Name: "mac"}}
	got, err := parseWakeHomePresence(machines, "http://203.0.113.2:18780/v1/presence", "tok", "mac")
	if err != nil {
		t.Fatal(err)
	}
	if got.URL == "" || got.Token != "tok" {
		t.Fatalf("解析结果不对: %+v", got)
	}
	if _, ok := got.Machines["mac"]; !ok {
		t.Fatal("应包含 mac")
	}
	if _, ok := got.Machines["linux"]; ok {
		t.Fatal("不应包含 linux")
	}

	if _, err := parseWakeHomePresence(machines, "http://x", "", "mac"); err == nil {
		t.Fatal("缺 token 应失败")
	}
	if _, err := parseWakeHomePresence(machines, "", "tok", "mac"); err == nil {
		t.Fatal("缺 URL 应失败")
	}
	if _, err := parseWakeHomePresence(machines, "http://x", "tok", "tokyo"); err == nil {
		t.Fatal("未知机器应失败")
	}
	empty, err := parseWakeHomePresence(machines, "", "", "")
	if err != nil || empty.URL != "" {
		t.Fatalf("全空应关闭功能: %+v %v", empty, err)
	}
}

func TestWakeFailureMessage(t *testing.T) {
	if msg := wakeFailureMessage("mac", ErrWakeSuppressedAtHome); msg == "mac 未能自动唤醒；请确认机器已接电且未关机" {
		t.Fatalf("在家抑制应有专用文案，得到 %q", msg)
	}
	if msg := wakeFailureMessage("mac", errors.New("boom")); msg != "mac 未能自动唤醒；请确认机器已接电且未关机" {
		t.Fatalf("普通失败文案不对: %q", msg)
	}
}
