package server

import (
	"os"
	"strings"
	"testing"
)

func TestParseEdgeHosts(t *testing.T) {
	edges, err := parseEdgeHosts("hk=hk.shell.example.com,edge-b=edge-b.shell.example.com")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(edges) != 2 || edges[0].Name != "hk" || edges[0].Host != "hk.shell.example.com" {
		t.Fatalf("意外结果: %+v", edges)
	}
}

func TestParseEdgeHostsRejectsSingle(t *testing.T) {
	if _, err := parseEdgeHosts("hk=hk.shell.example.com"); err == nil {
		t.Fatal("单个边缘应失败")
	}
}

func TestParseEdgeHostsRejectsURL(t *testing.T) {
	if _, err := parseEdgeHosts("hk=https://hk.shell.example.com,edge-b=edge-b.shell.example.com"); err == nil {
		t.Fatal("带 scheme 应失败")
	}
}

func TestConfigFromEnvRequiresCookieDomainWithEdges(t *testing.T) {
	t.Setenv("SHELL_ALLOWED_EMAILS", "operator@example.com")
	t.Setenv("AUTH_JWT_SECRET", testSecret)
	t.Setenv("AUTH_JWT_ISSUERS", "https://auth.example.com")
	t.Setenv("SHELL_SUPABASE_URL", "https://auth.example")
	t.Setenv("SHELL_SUPABASE_ANON_KEY", "anon")
	t.Setenv("SHELL_TTYD_URL", "http://127.0.0.1:7681")
	t.Setenv("SHELL_DATABASE_URL", "./data/dev.db")
	t.Setenv("SHELL_EDGE_HOSTS", "hk=hk.shell.example.com,edge-b=edge-b.shell.example.com")
	t.Setenv("SHELL_COOKIE_DOMAIN", "")
	_ = os.Unsetenv("SHELL_COOKIE_DOMAIN")
	_, err := ConfigFromEnv()
	if err == nil || !strings.Contains(err.Error(), "SHELL_COOKIE_DOMAIN") {
		t.Fatalf("期望要求 Cookie Domain，得到 %v", err)
	}
}

func TestConfigFromEnvEdgesOK(t *testing.T) {
	t.Setenv("SHELL_ALLOWED_EMAILS", "operator@example.com")
	t.Setenv("AUTH_JWT_SECRET", testSecret)
	t.Setenv("AUTH_JWT_ISSUERS", "https://auth.example.com")
	t.Setenv("SHELL_SUPABASE_URL", "https://auth.example")
	t.Setenv("SHELL_SUPABASE_ANON_KEY", "anon")
	t.Setenv("SHELL_TTYD_URL", "http://127.0.0.1:7681")
	t.Setenv("SHELL_DATABASE_URL", "./data/dev.db")
	t.Setenv("SHELL_EDGE_HOSTS", "hk=hk.shell.example.com,edge-b=edge-b.shell.example.com")
	t.Setenv("SHELL_COOKIE_DOMAIN", "shell.example.com")
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("应成功: %v", err)
	}
	if !cfg.EdgeRoutingEnabled() || cfg.ApexOrigin() != "https://shell.example.com" {
		t.Fatalf("边缘选路未生效: domain=%q edges=%+v", cfg.CookieDomain, cfg.Edges)
	}
	if !cfg.IsEdgeHost("hk.shell.example.com:443") || !cfg.IsApexHost("shell.example.com") {
		t.Fatal("Host 判定失败")
	}
}

func TestStripHostPort(t *testing.T) {
	if got := stripHostPort("hk.shell.example.com:443"); got != "hk.shell.example.com" {
		t.Fatalf("got %q", got)
	}
	if got := stripHostPort("[::1]:18765"); got != "::1" {
		t.Fatalf("got %q", got)
	}
}

func TestLocalConfigRequiresDomainForPasskeys(t *testing.T) {
	t.Setenv("SHELL_AUTH_MODE", "local")
	t.Setenv("SHELL_ALLOWED_EMAILS", "user@example.com")
	t.Setenv("SHELL_TTYD_URL", "http://127.0.0.1:7681")
	t.Setenv("SHELL_EDGE_HOSTS", "")
	t.Setenv("SHELL_COOKIE_DOMAIN", "")
	for _, origin := range []string{"http://127.0.0.1:18765", "https://192.0.2.1", "http://[::1]:18765"} {
		t.Setenv("SHELL_PUBLIC_URL", origin)
		if _, err := ConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "localhost") {
			t.Fatalf("unsupported local IP origin: %s: %v", origin, err)
		}
	}
	t.Setenv("SHELL_PUBLIC_URL", "http://localhost:18765")
	if _, err := ConfigFromEnv(); err != nil {
		t.Fatal(err)
	}
}

func TestAdditionalSSOConfiguration(t *testing.T) {
	t.Setenv("SHELL_TTYD_URL", "http://127.0.0.1:7681")
	t.Setenv("SHELL_AUTH_MODE", "supabase")
	t.Setenv("SHELL_SUPABASE_URL", "https://auth.example.com")
	t.Setenv("SHELL_SUPABASE_ANON_KEY", "public-anon")
	t.Setenv("AUTH_JWT_SECRET", testSecret)
	t.Setenv("AUTH_JWT_ISSUERS", "https://auth.example.com")
	t.Setenv("SHELL_ALLOWED_EMAILS", "operator@example.com")
	t.Setenv("SHELL_EDGE_HOSTS", "")
	t.Setenv("SHELL_PUBLIC_URL", "https://shell.example.com")
	t.Setenv("SHELL_SSO_ENABLED", "true")
	t.Setenv("SHELL_SSO_ISSUER", "https://login.example.com")
	t.Setenv("SHELL_SSO_CLIENT_ID", "gateway")
	t.Setenv("SHELL_SSO_CLIENT_SECRET", "test-confidential-client")
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthMode != "supabase" || !cfg.SSOEnabled || cfg.SupabaseURL != "https://auth.example.com" {
		t.Fatalf("primary authentication was replaced: %+v", cfg.AuthMode)
	}
	t.Setenv("SHELL_SSO_CLIENT_SECRET", "")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("enabled incomplete SSO must fail closed")
	}
	t.Setenv("SHELL_SSO_ENABLED", "false")
	if cfg, err := ConfigFromEnv(); err != nil || cfg.SSOEnabled {
		t.Fatalf("disabled SSO must leave primary provider available: %v", err)
	}
	t.Setenv("SHELL_SSO_ENABLED", "invalid")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("invalid enabled flag accepted")
	}
}
