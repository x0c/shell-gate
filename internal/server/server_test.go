package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	authkit "github.com/x0c/shell-gate/internal/auth"
	"github.com/x0c/shell-gate/internal/server/store"

	"github.com/golang-jwt/jwt/v5"
)

var testSecret = strings.Repeat("test-only-", 4)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	ttyd, _ := url.Parse("http://127.0.0.1:65535")
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	st.Defaults = []store.DefaultPreset{{Name: "example-tui", Command: "example-tui"}, {Name: "example-task", Command: "example-task"}}
	t.Cleanup(func() { _ = st.Close() })
	cfg := Config{
		Listen:        "127.0.0.1:0",
		AllowedEmails: map[string]struct{}{"operator@example.com": {}},
		SupabaseURL:   "https://auth.example.com",
		SupabaseAnon:  "anon",
		DebugLogs:     true,
		Machines:      []MachineConfig{{Name: "default", URL: ttyd}},
		CookieName:    "shell_session",
		CookieSecure:  false,
		SessionTTL:    time.Hour,
		DataDir:       dir,
		DatabaseURL:   filepath.Join(dir, "test.db"),
		// 单测不起后台探测循环，只保留被动统计（拨号采样 / 字节计数）。
		NetmonInterval: 0,
	}
	verifier, err := authkit.New(authkit.Config{
		Secret:   []byte(testSecret),
		Issuers:  []string{"https://auth.example.com"},
		Audience: "authenticated",
	})
	if err != nil {
		t.Fatalf("构造 verifier 失败: %v", err)
	}
	s := &Server{
		cfg:             cfg,
		verifier:        verifier,
		sessionSecret:   []byte(testSecret),
		sessionIssuer:   "https://auth.example.com",
		sessionAudience: "authenticated",
		store:           st,
		tabs:            mustTabStore(t, st),
		ready:           true,
	}
	s.initUpstreams()
	return s
}

func mustTabStore(t *testing.T, db *store.Store) *tabStore {
	t.Helper()
	ts, err := newTabStore(db)
	if err != nil {
		t.Fatalf("构造 Tab 存储失败: %v", err)
	}
	return ts
}

func mustCreateTab(t *testing.T, ts *tabStore, email, machine, title, command string) *TabSession {
	t.Helper()
	tab, err := ts.Create(email, machine, title, command)
	if err != nil {
		t.Fatalf("创建 Tab 失败: %v", err)
	}
	return tab
}

func signToken(t *testing.T, email string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss":   "https://auth.example.com",
		"aud":   "authenticated",
		"sub":   "user-123",
		"email": email,
		"exp":   time.Now().Add(time.Hour).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("签发测试 token 失败: %v", err)
	}
	return s
}

func withSession(req *http.Request, token string) *http.Request {
	req.AddCookie(&http.Cookie{Name: "shell_session", Value: token})
	return req
}

func TestSessionAllowedEmail(t *testing.T) {
	s := newTestServer(t)
	token := signToken(t, "operator@example.com")
	body, _ := json.Marshal(sessionRequest{AccessToken: token})
	req := httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	s.handleSession(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d，body=%s", w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) == 0 {
		t.Fatal("期望写入 Cookie，但没有")
	}
}

func TestSessionMintsLongLivedCookie(t *testing.T) {
	s := newTestServer(t)
	s.cfg.SessionTTL = 12 * time.Hour
	// 模拟 GoTrue 短寿命 access_token（约 1h）
	shortClaims := jwt.MapClaims{
		"iss":   "https://auth.example.com",
		"aud":   "authenticated",
		"sub":   "user-123",
		"email": "operator@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, shortClaims)
	access, err := tok.SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("签发短寿命 token 失败: %v", err)
	}
	body, _ := json.Marshal(sessionRequest{AccessToken: access})
	req := httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	s.handleSession(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d，body=%s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("期望写入 Cookie，但没有")
	}
	c := cookies[0]
	if c.MaxAge != int((12 * time.Hour).Seconds()) {
		t.Fatalf("Cookie MaxAge 期望 %d，得到 %d", int((12 * time.Hour).Seconds()), c.MaxAge)
	}
	if c.Value == access {
		t.Fatal("会话 Cookie 不应继续使用短寿命 access_token")
	}
	id, err := s.verifier.Verify(c.Value)
	if err != nil {
		t.Fatalf("会话 Cookie 应可校验: %v", err)
	}
	if id.Email != "operator@example.com" {
		t.Fatalf("email=%q", id.Email)
	}
	exp, err := id.Claims.GetExpirationTime()
	if err != nil || exp == nil {
		t.Fatalf("会话 Cookie 缺少 exp: %v", err)
	}
	remaining := time.Until(exp.Time)
	if remaining < 11*time.Hour || remaining > 12*time.Hour+time.Minute {
		t.Fatalf("会话 JWT 剩余寿命应约为 12h，得到 %v", remaining)
	}
}

func TestSessionRejectsOtherEmail(t *testing.T) {
	s := newTestServer(t)
	token := signToken(t, "someone@else.com")
	body, _ := json.Marshal(sessionRequest{AccessToken: token})
	req := httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	s.handleSession(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("非白名单邮箱应 403，得到 %d", w.Code)
	}
}

func TestSessionRejectsBadToken(t *testing.T) {
	s := newTestServer(t)
	body, _ := json.Marshal(sessionRequest{AccessToken: "not-a-jwt"})
	req := httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	s.handleSession(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无效 token 应 401，得到 %d", w.Code)
	}
}

func TestRootRedirectsWithoutCookie(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	s.handleRoot(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("无会话应重定向到登录，得到 %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/login" {
		t.Fatalf("期望跳 /login，得到 %q", loc)
	}
}

func TestRootServesWorkspace(t *testing.T) {
	s := newTestServer(t)
	token := signToken(t, "operator@example.com")
	req := withSession(httptest.NewRequest(http.MethodGet, "/", nil), token)
	w := httptest.NewRecorder()
	s.handleRoot(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("已登录应返回工作台，得到 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `id="launchers"`) {
		t.Fatal("工作台 HTML 缺少顶栏快捷Launch区")
	}
	if got := w.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
		t.Fatalf("工作台应禁止被 iframe 嵌套，CSP=%q", got)
	}
	if got := w.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("工作台 X-Frame-Options 期望 DENY，得到 %q", got)
	}
}

func TestRootRejectsIframeEmbedding(t *testing.T) {
	s := newTestServer(t)
	token := signToken(t, "operator@example.com")
	// 空 path 拼出的 "?rendererType=…" 会解析成 /?…；浏览器以 iframe 拉取时必须拒绝，
	// 否则工作台嵌进Terminal → 双层 title bar。
	req := withSession(httptest.NewRequest(http.MethodGet, "/?rendererType=canvas&disableReconnect=true", nil), token)
	req.Header.Set("Sec-Fetch-Dest", "iframe")
	w := httptest.NewRecorder()
	s.handleRoot(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("iframe 拉取工作台应 403，得到 %d", w.Code)
	}
	if strings.Contains(w.Body.String(), `id="launchers"`) {
		t.Fatal("iframe 响应不得再下发工作台 HTML")
	}
}

func TestLoginDeniesFraming(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	w := httptest.NewRecorder()
	s.handleLogin(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Not signed in应返回登录页，得到 %d", w.Code)
	}
	if got := w.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
		t.Fatalf("登录页应禁止被 iframe 嵌套，CSP=%q", got)
	}
}

func TestWorkspaceHTMLGuardsAgainstNestedShellAndInvalidTabPath(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("web", "workspace.html"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	for _, want := range []string{
		`window.top !== window.self`,
		`workspace must not run inside an iframe`,
		`/^\/t\/[^/]+\/$/.test(path)`,
		`The terminal address is invalid. Close it and open it again.`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("双层顶栏守卫缺失: %q", want)
		}
	}
}

func TestProxyRewriteInjectsTrustedAuthHeaderAndCommand(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req = req.WithContext(context.WithValue(req.Context(), proxyUserKey{}, "operator@example.com"))
	req = req.WithContext(context.WithValue(req.Context(), proxyCmdKey{}, "example-tui"))
	req.Header.Set(authHeaderName, "attacker@evil.com")
	req.URL.RawQuery = "arg=evil"
	out := req.Clone(req.Context())
	s.primaryUpstream().proxy.(*httputil.ReverseProxy).Rewrite(&httputil.ProxyRequest{In: req, Out: out})
	if got := out.Header.Get(authHeaderName); got != "operator@example.com" {
		t.Fatalf("期望注入已校验邮箱，得到 %q", got)
	}
	// Command要包成「登录 + 交互」shell，PATH/别名才和用户手敲一致；Presets与收尾都跟 $SHELL。
	if got := out.URL.Query().Get("arg"); got != `exec "$SHELL" -lic $'example-tui\nexec "$SHELL" -li'` {
		t.Fatalf("期望注入交互式登录 shell 包装后的 example-tui，得到 %q", got)
	}
}

func TestInteractiveLoginCommandQuoting(t *testing.T) {
	cases := map[string]string{
		"example-tui":        `exec "$SHELL" -lic $'example-tui\nexec "$SHELL" -li'`,
		`echo "hi"`:          `exec "$SHELL" -lic $'echo "hi"\nexec "$SHELL" -li'`,
		`echo 'a' && echo b`: `exec "$SHELL" -lic $'echo \'a\' && echo b\nexec "$SHELL" -li'`,
		`grep '\d' x`:        `exec "$SHELL" -lic $'grep \'\\d\' x\nexec "$SHELL" -li'`,
		// 空Command也要注入，否则上游开 bash -l，Mac 进不了默认 zsh。
		"": `exec "$SHELL" -li`,
	}
	for in, want := range cases {
		if got := interactiveLoginCommand(in); got != want {
			t.Fatalf("包装 %q 得到 %q，期望 %q", in, got, want)
		}
	}
}

// 最终交互壳必须跟账号 $SHELL，才能还原本机插件（Mac 默认 shell、Linux 侧行编辑增强）。
// 2026-09-05 起禁止再导出 SHELLGATE_WEB_TERM 去跳过插件。
func TestInteractiveLoginCommandUsesLoginShell(t *testing.T) {
	for _, cmd := range []string{"", "example-tui", "example-task"} {
		got := interactiveLoginCommand(cmd)
		if strings.Contains(got, "SHELLGATE_WEB_TERM") {
			t.Fatalf("Command %q 不得再导出网页Terminal跳过标记，得到 %q", cmd, got)
		}
		if strings.Contains(got, "exec bash -lic") {
			t.Fatalf("Command %q 不得写死 bash -lic（Mac Presets会进错壳），得到 %q", cmd, got)
		}
		if !strings.Contains(got, `exec "$SHELL" -li`) {
			t.Fatalf("Command %q 的包装应落回 $SHELL，得到 %q", cmd, got)
		}
		if cmd != "" && !strings.Contains(got, `exec "$SHELL" -lic`) {
			t.Fatalf("Command %q 的Presets段也应跟 $SHELL -lic，得到 %q", cmd, got)
		}
	}
}

// 空Command同样要注入 arg：不注入的话上游脚本会自己开 bash -l，丢掉用户默认壳。
func TestProxyInjectsArgForEmptyCommand(t *testing.T) {
	if got := interactiveLoginCommand(""); got != `exec "$SHELL" -li` {
		t.Fatalf("空Command应包装成跟 $SHELL 的交互式登录 shell，得到 %q", got)
	}
}

// 一次性Command跑完后必须落回交互 shell，否则Terminal进程退出会触发自动重连死循环。
func TestInteractiveLoginCommandKeepsShellAlive(t *testing.T) {
	got := interactiveLoginCommand("example-task")
	if !strings.HasSuffix(got, `\nexec "$SHELL" -li'`) {
		t.Fatalf("Command包装缺少收尾的交互 shell，得到 %q", got)
	}
}

// 注入给 ttyd 的 arg 里绝不能出现真实控制字符：URL 里带 %0A 会让 libwebsockets
// 直接判握手失败，所有带Command的 Tab 都连不上（2026-08-03 线上实测）。
func TestInteractiveLoginCommandHasNoRawControlChars(t *testing.T) {
	got := interactiveLoginCommand("example-task")
	if strings.ContainsAny(got, "\n\r\t") {
		t.Fatalf("包装结果不得含真实换行/回车/制表符，得到 %q", got)
	}
	if escaped := url.QueryEscape(got); strings.Contains(escaped, "%0A") {
		t.Fatalf("URL 编码后不得出现 %%0A，得到 %q", escaped)
	}
}

func TestCreateTabAndTermRequiresSession(t *testing.T) {
	s := newTestServer(t)
	token := signToken(t, "operator@example.com")
	_ = s.store.EnsureDefaults("operator@example.com")
	list, err := s.store.List("operator@example.com")
	if err != nil || len(list) < 2 {
		t.Fatalf("默认Presets应包含 example-tui/example-task: %v %#v", err, list)
	}
	req := withSession(httptest.NewRequest(http.MethodPost, "/api/tabs", strings.NewReader(`{"presetId":"`+list[0].ID+`"}`)), token)
	w := httptest.NewRecorder()
	s.handleCreateTab(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("创建 Tab 应 200，得到 %d %s", w.Code, w.Body.String())
	}
	var tab map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &tab)
	id, _ := tab["id"].(string)
	if id == "" {
		t.Fatal("缺少 tab id")
	}
	// 无 Cookie 访问Terminal应 302 登录
	req2 := httptest.NewRequest(http.MethodGet, "/t/"+id+"/", nil)
	w2 := httptest.NewRecorder()
	s.handleRoot(w2, req2)
	if w2.Code != http.StatusFound {
		t.Fatalf("无 Cookie 访问 /t 应跳登录，得到 %d", w2.Code)
	}
}

func TestLogoutClearsCookie(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	w := httptest.NewRecorder()
	s.handleLogout(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登出应 200，得到 %d", w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) == 0 || cookies[0].MaxAge >= 0 {
		t.Fatal("登出应下发失效 Cookie（MaxAge<0）")
	}
}

func withEdgeRouting(s *Server) {
	s.cfg.CookieDomain = "shell.example.com"
	s.cfg.CookieSecure = true
	s.cfg.Edges = []EdgeHost{
		{Name: "hk", Host: "hk.shell.example.com"},
		{Name: "edge-b", Host: "edge-b.shell.example.com"},
	}
}

func TestSessionCookieWritesDomain(t *testing.T) {
	s := newTestServer(t)
	withEdgeRouting(s)
	token := signToken(t, "operator@example.com")
	body, _ := json.Marshal(sessionRequest{AccessToken: token})
	req := httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	s.handleSession(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d", w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Domain != "shell.example.com" {
		t.Fatalf("会话 Cookie 应带 Domain=shell.example.com，得到 %+v", cookies)
	}
}

func TestLogoutClearsCookieWithDomain(t *testing.T) {
	s := newTestServer(t)
	withEdgeRouting(s)
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	w := httptest.NewRecorder()
	s.handleLogout(w, req)
	cookies := w.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Domain != "shell.example.com" || cookies[0].MaxAge >= 0 {
		t.Fatalf("登出 Cookie 必须同 Domain 且失效，得到 %+v", cookies)
	}
}

func TestConfigExposesEdges(t *testing.T) {
	s := newTestServer(t)
	withEdgeRouting(s)
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	w := httptest.NewRecorder()
	s.handleConfig(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析 config: %v", err)
	}
	if body["apexOrigin"] != "https://shell.example.com" {
		t.Fatalf("apexOrigin=%v", body["apexOrigin"])
	}
	edges, ok := body["edges"].([]any)
	if !ok || len(edges) != 2 {
		t.Fatalf("edges=%v", body["edges"])
	}
}

func TestEdgeProbeCORS(t *testing.T) {
	s := newTestServer(t)
	withEdgeRouting(s)

	req := httptest.NewRequest(http.MethodGet, "/api/edge-probe", nil)
	req.Header.Set("Origin", "https://shell.example.com")
	w := httptest.NewRecorder()
	s.handleEdgeProbe(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("期望 204，得到 %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://shell.example.com" {
		t.Fatalf("ACAO=%q", got)
	}

	bad := httptest.NewRequest(http.MethodGet, "/api/edge-probe", nil)
	bad.Header.Set("Origin", "https://evil.example")
	bw := httptest.NewRecorder()
	s.handleEdgeProbe(bw, bad)
	if bw.Code != http.StatusForbidden {
		t.Fatalf("非法 Origin 应 403，得到 %d", bw.Code)
	}
}

func TestEdgeProbeDisabledWithoutEdges(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/edge-probe", nil)
	w := httptest.NewRecorder()
	s.handleEdgeProbe(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("未配置边缘时 probe 应 404，得到 %d", w.Code)
	}
}

func TestRootApexServesEdgePick(t *testing.T) {
	s := newTestServer(t)
	withEdgeRouting(s)
	token := signToken(t, "operator@example.com")
	req := withSession(httptest.NewRequest(http.MethodGet, "/", nil), token)
	req.Host = "shell.example.com"
	w := httptest.NewRecorder()
	s.handleRoot(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("apex 已登录应返回选路页，得到 %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "正在选择更快的入口") || strings.Contains(body, `id="launchers"`) {
		t.Fatal("apex 应出选路页而非工作台")
	}
}

func TestRootEdgeServesWorkspace(t *testing.T) {
	s := newTestServer(t)
	withEdgeRouting(s)
	token := signToken(t, "operator@example.com")
	req := withSession(httptest.NewRequest(http.MethodGet, "/", nil), token)
	req.Host = "hk.shell.example.com"
	w := httptest.NewRecorder()
	s.handleRoot(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="launchers"`) {
		t.Fatalf("边缘应返回工作台，code=%d", w.Code)
	}
}

func TestRootEdgeUnauthRedirectsToApex(t *testing.T) {
	s := newTestServer(t)
	withEdgeRouting(s)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "edge-b.shell.example.com"
	w := httptest.NewRecorder()
	s.handleRoot(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("期望 302，得到 %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "https://shell.example.com/login" {
		t.Fatalf("Location=%q", loc)
	}
}

func TestLoginOnEdgeRedirectsToApex(t *testing.T) {
	s := newTestServer(t)
	withEdgeRouting(s)
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.Host = "hk.shell.example.com"
	w := httptest.NewRecorder()
	s.handleLogin(w, req)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "https://shell.example.com/login" {
		t.Fatalf("边缘 /login 应跳 apex，code=%d loc=%q", w.Code, w.Header().Get("Location"))
	}
}

func TestApexTermPathRedirectsToPick(t *testing.T) {
	s := newTestServer(t)
	withEdgeRouting(s)
	token := signToken(t, "operator@example.com")
	req := withSession(httptest.NewRequest(http.MethodGet, "/t/abc/", nil), token)
	req.Host = "shell.example.com"
	w := httptest.NewRecorder()
	s.handleRoot(w, req)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Fatalf("apex /t 应回选路，code=%d loc=%q", w.Code, w.Header().Get("Location"))
	}
}

func TestHealthz(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	s.handleHealthz(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("健康检查应 200，得到 %d", w.Code)
	}
}

func TestInjectEnhanceScriptPlainHTML(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:   io.NopCloser(strings.NewReader("<html><body><p>hi</p></body></html>")),
	}
	if err := injectEnhanceScript(resp); err != nil {
		t.Fatalf("注入失败: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), enhanceScriptTag) {
		t.Fatalf("HTML 中未找到注入的脚本标签: %s", body)
	}
	if got := resp.Header.Get("Content-Length"); got != strconv.Itoa(len(body)) {
		t.Fatalf("Content-Length 未同步更新: got=%q want=%d", got, len(body))
	}
}

func TestInjectEnhanceScriptGzipHTML(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	_, _ = gw.Write([]byte("<html><body>hi</body></html>"))
	_ = gw.Close()
	resp := &http.Response{
		Header: http.Header{
			"Content-Type":     []string{"text/html; charset=utf-8"},
			"Content-Encoding": []string{"gzip"},
		},
		Body: io.NopCloser(bytes.NewReader(buf.Bytes())),
	}
	if err := injectEnhanceScript(resp); err != nil {
		t.Fatalf("注入失败: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	gr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("响应体应仍是合法 gzip: %v", err)
	}
	decoded, _ := io.ReadAll(gr)
	if !strings.Contains(string(decoded), enhanceScriptTag) {
		t.Fatalf("解压后未找到注入的脚本标签: %s", decoded)
	}
}

func TestInjectEnhanceScriptSkipsNonHTML(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   io.NopCloser(strings.NewReader(`{"ok":true}`)),
	}
	if err := injectEnhanceScript(resp); err != nil {
		t.Fatalf("非 HTML 响应不应报错: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"ok":true}` {
		t.Fatalf("非 HTML 响应体不应被改动，得到: %s", body)
	}
}

func TestRootServesWorkspaceWithCanvasRendererQuery(t *testing.T) {
	s := newTestServer(t)
	token := signToken(t, "operator@example.com")
	req := withSession(httptest.NewRequest(http.MethodGet, "/", nil), token)
	w := httptest.NewRecorder()
	s.handleRoot(w, req)
	body := w.Body.String()
	// 2026-08-31：默认 canvas，避开 ttyd 1.7.7 / xterm 5.4.0 WebGL 图集把
	// CJK 画丢的问题。URL 查询参数优先于 ttyd Launch参数。禁止为了 44ms 绘制
	// 延迟把默认改回 webgl（见 docs/troubleshooting/2026-08-20-tui-display-corruption.md）。
	if !strings.Contains(body, "?rendererType=canvas") {
		t.Fatal("工作台 HTML 缺少 iframe 强制 canvas 渲染器的查询参数")
	}
	if strings.Contains(body, "rendererType=webgl") {
		t.Fatal("工作台不应再强制 webgl 渲染器（CJK 图集花屏，2026-08-31 改回 canvas）")
	}
}

func TestRootServesWorkspaceWithTranslucentSelectionTheme(t *testing.T) {
	// canvas 渲染器把选区画在文字层上面的独立图层，selectionBackground 若是
	// 不透明色会整块盖住底下的文字（xterm.js 官方 issue #2737/#2808 确认的已知
	// 行为，2026-07-20 真机复现）；iframe 必须带一个带 alpha 通道的
	// selectionBackground，纯色（不带 8 位十六进制透明度）视为回归。
	s := newTestServer(t)
	token := signToken(t, "operator@example.com")
	req := withSession(httptest.NewRequest(http.MethodGet, "/", nil), token)
	w := httptest.NewRecorder()
	s.handleRoot(w, req)
	body := w.Body.String()
	if !strings.Contains(body, "selectionBackground") {
		t.Fatal("工作台 HTML 缺少 iframe 的 selectionBackground 主题覆盖，canvas 渲染器下拖选会整块盖住文字")
	}
	// theme 值在浏览器里经 encodeURIComponent(JSON.stringify(...)) 运行时拼接，
	// 静态 HTML 源码里看到的是字面量，直接核对字面量里的颜色带 8 位十六进制
	// （含 alpha 通道），不是 6 位纯色。
	if !regexp.MustCompile(`selectionBackground"?\s*:\s*"#[0-9A-Fa-f]{8}"`).MatchString(body) {
		t.Fatal("selectionBackground 必须是带 8 位十六进制（含 alpha 通道）的半透明色，纯色会盖住文字")
	}
}

func TestRootServesWorkspaceWithBothTerminalColorSchemes(t *testing.T) {
	// 深LightSystem/手动切换（2026-08 上线）：页面必须同时带深、浅两套完整的
	// xterm Terminal配色常量，防止后续改动误删其中一套——那样切换要么没效果（缺Light），
	// 要么切回Dark也回不去原来的观感（缺Dark兜底）。
	s := newTestServer(t)
	token := signToken(t, "operator@example.com")
	req := withSession(httptest.NewRequest(http.MethodGet, "/", nil), token)
	w := httptest.NewRecorder()
	s.handleRoot(w, req)
	body := w.Body.String()

	if !strings.Contains(body, "TERM_THEME_DARK") || !strings.Contains(body, "TERM_THEME_LIGHT") {
		t.Fatal("工作台 HTML 缺少Dark/LightTerminal配色常量之一")
	}
	// Light配色的底/字/强调三个关键色，任一缺失都说明Light配色被误删或改坏。
	for _, want := range []string{"#FAFAF8", "#24292F", "#2F8F63"} {
		if !strings.Contains(body, want) {
			t.Fatalf("工作台 HTML 缺少LightTerminal配色的关键颜色 %s", want)
		}
	}
	// 所有 selectionBackground 字面量（Dark一份、Light一份）都必须带 8 位十六进制
	// alpha 通道，不能有任何一份退化成不带透明度的纯色。
	matches := regexp.MustCompile(`selectionBackground"?\s*:\s*"#([0-9A-Fa-f]+)"`).FindAllStringSubmatch(body, -1)
	if len(matches) < 2 {
		t.Fatalf("期望至少 2 处 selectionBackground（Dark + Light），实际找到 %d 处", len(matches))
	}
	for _, m := range matches {
		if len(m[1]) != 8 {
			t.Fatalf("selectionBackground 颜色 #%s 不是 8 位十六进制（含 alpha 通道），canvas 渲染器下会盖住文字", m[1])
		}
	}
}

func TestHandleEnhanceJSServesScript(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/__shellgate/enhance.js", nil)
	w := httptest.NewRecorder()
	s.handleEnhanceJS(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "CORRAL_IMG_BEGIN") {
		t.Fatal("增强脚本内容缺少图片Paste哨兵标记")
	}
	// WebGL 截图兼容：有人强制 webgl 或上下文丢失后再拉起时，必须在建上下文前
	// 强制 preserveDrawingBuffer，否则 captureTerminalCanvas 合成出来是全黑。
	// 默认渲染器已改回 canvas（2026-08-31），这个补丁仍要留着。
	if !strings.Contains(body, "preserveDrawingBuffer") {
		t.Fatal("增强脚本缺少 WebGL preserveDrawingBuffer 补丁，截图会得到全黑画面")
	}
	if !strings.Contains(body, "forcePreserveDrawingBufferForWebGL") {
		t.Fatal("增强脚本缺少 forcePreserveDrawingBufferForWebGL 安装点")
	}
	if !strings.Contains(body, "shellgate-mobile-bar") {
		t.Fatal("增强脚本缺少手机快捷键栏")
	}
	if !strings.Contains(body, "(pointer: coarse)") || !strings.Contains(body, "(hover: none)") {
		t.Fatal("手机栏启用条件必须是 pointer:coarse + hover:none，避免桌面触控板/窄窗误开")
	}
	idx := strings.Index(body, "function shouldEnableMobileUX")
	if idx < 0 {
		t.Fatal("缺少 shouldEnableMobileUX")
	}
	end := strings.Index(body[idx:], "\n  function ")
	if end < 0 {
		end = len(body) - idx
	}
	fn := body[idx : idx+end]
	for _, bad := range []string{"ontouchstart", "maxTouchPoints", "max-width: 900px"} {
		if strings.Contains(fn, bad) {
			t.Fatalf("shouldEnableMobileUX 不得再用 %q（会在桌面误开快捷键栏）", bad)
		}
	}
	if !strings.Contains(body, "installTouchScroll") {
		t.Fatal("增强脚本缺少触摸滚动")
	}
	if !strings.Contains(body, "mobile-tap") {
		t.Fatal("增强脚本缺少 mobile-tap 埋点")
	}
	if !strings.Contains(body, "pointerdown") || !strings.Contains(body, "pointerup") {
		t.Fatal("手机快捷键应在按下时保留Terminal焦点、抬起时执行，避免触摸重复触发或吞键")
	}
	if !strings.Contains(body, "sendMouseWheel") {
		t.Fatal("增强脚本缺少 SGR 滚轮回退（全屏 TUI 触摸滑动）")
	}
	if !strings.Contains(body, "sg-row") {
		t.Fatal("手机快捷键栏应拆成两行 .sg-row，避免 flex-wrap 量高不准盖住第二行")
	}
	if !strings.Contains(body, "sg-key") {
		t.Fatal("手机快捷键应使用 .sg-key")
	}
	if !strings.Contains(body, "shellgate-bleed-style") || !strings.Contains(body, "#terminal-container .terminal") {
		t.Fatal("应覆盖 ttyd 默认 .terminal{padding:5px}，否则四周会留一圈边框")
	}
	if !strings.Contains(body, "tightFitTerminal") || !strings.Contains(body, "terminal-tight-fit") {
		t.Fatal("应有 tightFit：补 FitAddon 少算的列/行并对半格余量 scale，消掉右/下空带")
	}
	if !strings.Contains(body, "overflow-y:auto") {
		t.Fatal("xterm-viewport 应为 overflow-y:auto，scroll 会预留右侧滚动条槽")
	}
	if !strings.Contains(body, "document.createElement(\"button\")") || !strings.Contains(body, "btn.tabIndex = -1") {
		t.Fatal("快捷键应使用不接管焦点的真实按钮")
	}
	if !strings.Contains(body, "keyboardHeldOpen") || !strings.Contains(body, `d.__shellgate === "keyboard"`) {
		t.Fatal("Terminal页必须接收父页键盘开合Status，已打开时点Terminal不得再 blur 把键盘闪掉")
	}
	if !strings.Contains(body, "was && keyboardHeldOpen") {
		t.Fatal("键盘已开且输入框仍聚焦时必须跳过 blur/focus")
	}
	if !strings.Contains(body, "shouldSkipMobileKeyboardViewportFit") {
		t.Fatal("手机端宽度没变时不得把 visualViewport resize 当成一次 fit")
	}
	if !strings.Contains(body, "body{display:flex") || !strings.Contains(body, "flex:1 1 auto") {
		t.Fatal("工具栏与Terminal应参与同一个纵向排版，工具栏不得覆盖Terminal画面")
	}
	for _, bad := range []string{"setAttribute(\"for\"", "mobileViewportBox", "GROW_CONFIRM_MS", "shellgate-mobile-viewport", "MutationObserver"} {
		if strings.Contains(body, bad) {
			t.Fatalf("手机工具栏不得再依赖旧的焦点/视口猜测机制 %q", bad)
		}
	}
	if !strings.Contains(body, "overflow:hidden") {
		t.Fatal("Terminal容器应 overflow:hidden，避免 tightFit scale 溢出盖住快捷键第二行")
	}
	if !strings.Contains(body, "fitBox = term.element") {
		t.Fatal("tightFit 应在手机栏模式下按 xterm 区域量高，避免 scale 盖住第二行")
	}
}

// 首屏黑边回归：ttyd 在 WebSocket 握手后才套用字号/渲染器，容器尺寸一动不动、
// 字符网格却整体重算。若冪等指纹只看容器像素盒，首屏那一次贴合就是最后一次，
// 右侧黑边会一直留到用户手动改窗口大小。
func TestEnhanceJSRefitsAfterTerminalGridChanges(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/__shellgate/enhance.js", nil)
	w := httptest.NewRecorder()
	s.handleEnhanceJS(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "tightFitStateKey") {
		t.Fatal("冪等指纹应独立成函数，并把字符网格纳入比较")
	}
	for _, needle := range []string{"term.cols || 0", "term.rows || 0", "screen.offsetWidth"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("tightFit 冪等指纹缺少网格维度 %q，容器不变但字号变化时会漏掉重算", needle)
		}
	}
	if !strings.Contains(body, "term.onResize(") {
		t.Fatal("应监听 xterm 尺寸变化，ttyd 握手后套用字号时立即重新贴合")
	}
	if !strings.Contains(body, "document.fonts.ready") {
		t.Fatal("应在字体加载完成后重新贴合，字体换算改变单元格宽度会留下右侧黑边")
	}
}

func TestEnhanceJSHandlesMacClipboardCommands(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/__shellgate/enhance.js", nil)
	w := httptest.NewRecorder()
	s.handleEnhanceJS(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d", w.Code)
	}
	body := w.Body.String()
	for _, needle := range []string{
		"\"copy\",\n      function",
		`clipboardData.setData("text/plain", text)`,
		"\"paste\",\n      function",
		`clipboardData.getData("text/plain")`,
		`term.paste(text)`,
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("增强脚本缺少 macOS 剪贴板兜底 %q", needle)
		}
	}
}

func TestWorkspaceHTMLMobileViewport(t *testing.T) {
	s := newTestServer(t)
	token := signToken(t, "operator@example.com")
	req := withSession(httptest.NewRequest(http.MethodGet, "/", nil), token)
	w := httptest.NewRecorder()
	s.handleRoot(w, req)
	body := w.Body.String()
	if !strings.Contains(body, "interactive-widget=resizes-content") {
		t.Fatal("工作台缺少 interactive-widget=resizes-content，Android/Chrome 键盘弹起时布局不会变矮")
	}
	if !strings.Contains(body, "visualViewport") {
		t.Fatal("工作台缺少 visualViewport 同步，iOS 键盘弹起时无法压矮页面")
	}
	if !strings.Contains(body, "--app-height") {
		t.Fatal("工作台缺少 --app-height 视口高度变量")
	}
	if !strings.Contains(body, "KEYBOARD_LOCK_SHRINK_PX") {
		t.Fatal("手机工作台必须在键盘升起后锁定高度，避免收起再弹起重绘Terminal")
	}
	if !strings.Contains(body, "preserveWorkspaceTerminalFocus") {
		t.Fatal("顶栏等非输入控件不得抢走Terminal焦点把键盘收掉")
	}
	if !strings.Contains(body, `__shellgate: "keyboard"`) {
		t.Fatal("父页必须把键盘开合告诉Terminal iframe")
	}
	if !strings.Contains(body, "focusActiveTerminal") {
		t.Fatal("关掉Presets面板后须把焦点交回当前Terminal")
	}
}

func TestHandleDebugLogRequiresSession(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/__shellgate/debug-log",
		strings.NewReader(`{"event":"punct-fix"}`))
	w := httptest.NewRecorder()
	s.handleDebugLog(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("Not signed in应 401，得到 %d", w.Code)
	}
}

func TestHandleDebugLogAppendsNdjson(t *testing.T) {
	s := newTestServer(t)
	token := signToken(t, "operator@example.com")
	req := withSession(httptest.NewRequest(http.MethodPost, "/__shellgate/debug-log",
		strings.NewReader(`{"event":"clipboard-write","detail":{"mode":"silent","ok":true}}`)), token)
	w := httptest.NewRecorder()
	s.handleDebugLog(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d %s", w.Code, w.Body.String())
	}
	raw, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "debug-log.ndjson"))
	if err != nil {
		t.Fatalf("读调试日志失败: %v", err)
	}
	var entry map[string]any
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("日志行不是合法 JSON: %v, raw=%s", err, raw)
	}
	if entry["event"] != "clipboard-write" {
		t.Fatalf("期望 event=clipboard-write，得到 %v", entry["event"])
	}
	if entry["email"] != "operator@example.com" {
		t.Fatalf("期望记录登录邮箱，得到 %v", entry["email"])
	}
	if entry["ts"] == nil || entry["ts"] == "" {
		t.Fatal("期望记录时间戳")
	}
}

func TestHandleDebugLogRejectsInvalidJSON(t *testing.T) {
	s := newTestServer(t)
	token := signToken(t, "operator@example.com")
	req := withSession(httptest.NewRequest(http.MethodPost, "/__shellgate/debug-log",
		strings.NewReader("not json")), token)
	w := httptest.NewRecorder()
	s.handleDebugLog(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应 400，得到 %d", w.Code)
	}
}

func TestHandleDebugLogPerfSampleGoesToPerfLog(t *testing.T) {
	s := newTestServer(t)
	token := signToken(t, "operator@example.com")
	body := `{"event":"perf-sample","detail":{"intervalMs":5000,"renderCount":3,"wsDownMsgs":10}}`
	req := withSession(httptest.NewRequest(http.MethodPost, "/__shellgate/debug-log",
		strings.NewReader(body)), token)
	w := httptest.NewRecorder()
	s.handleDebugLog(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(s.cfg.DataDir, "debug-log.ndjson")); !os.IsNotExist(err) {
		t.Fatal("perf-sample 不应写入 debug-log.ndjson")
	}
	raw, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "perf-log.ndjson"))
	if err != nil {
		t.Fatalf("读 perf-log 失败: %v", err)
	}
	var entry map[string]any
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("日志行不是合法 JSON: %v, raw=%s", err, raw)
	}
	if entry["event"] != "perf-sample" {
		t.Fatalf("期望 event=perf-sample，得到 %v", entry["event"])
	}
}

func TestRotateNdjsonLogIfNeeded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "perf-log.ndjson")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 64), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rotateNdjsonLogIfNeeded(path, 32); err != nil {
		t.Fatalf("轮转失败: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("轮转后原文件应已被挪走")
	}
	backup, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("读备份失败: %v", err)
	}
	if len(backup) != 64 {
		t.Fatalf("备份大小期望 64，得到 %d", len(backup))
	}
	// 再追加一行应创建新文件
	if err := appendNdjsonLogLine(dir, "perf-log.ndjson", []byte(`{"event":"perf-sample"}`)); err != nil {
		t.Fatalf("追加失败: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读新文件失败: %v", err)
	}
	if !strings.Contains(string(raw), "perf-sample") {
		t.Fatalf("新文件应含 perf-sample，得到 %s", raw)
	}
}

func TestEnhanceJSContainsPerfSampler(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/__shellgate/enhance.js", nil)
	w := httptest.NewRecorder()
	s.handleEnhanceJS(w, req)
	body := w.Body.String()
	for _, needle := range []string{"perf-sample", "installPerfSampler", "installWebSocketProbe"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("enhance.js 缺少 %q", needle)
		}
	}
}

// ---------- Network diagnostics ----------

func TestRTTWindowStats(t *testing.T) {
	w := newRTTWindow(4)
	if got := w.stats(); got.Count != 0 || got.P50Ms != 0 {
		t.Fatalf("空窗口应给全零，得到 %+v", got)
	}
	// 窗口容量 4，写 5 个Samples，最早的 10 应被挤掉。
	for _, v := range []float64{10, 40, 20, 30, 50} {
		w.add(v)
	}
	got := w.stats()
	if got.Count != 5 {
		t.Fatalf("累计Samples数应为 5，得到 %d", got.Count)
	}
	if got.MinMs != 20 || got.MaxMs != 50 {
		t.Fatalf("窗口内应只剩 20..50，得到 min=%v max=%v", got.MinMs, got.MaxMs)
	}
	if got.LastMs != 50 {
		t.Fatalf("最近Samples应为 50，得到 %v", got.LastMs)
	}
	if got.AvgMs != 35 {
		t.Fatalf("均值应为 35，得到 %v", got.AvgMs)
	}
	if got.P50Ms != 30 || got.P95Ms != 50 {
		t.Fatalf("分位数期望 p50=30 p95=50，得到 p50=%v p95=%v", got.P50Ms, got.P95Ms)
	}
	if got.JitterMs <= 0 {
		t.Fatal("Samples忽高忽低时Jitter必须为正，否则判不出「时快时慢」")
	}
	// 非法Samples不得污染统计
	w.add(-1)
	w.add(math.NaN())
	if after := w.stats(); after.Count != 5 {
		t.Fatalf("非法Samples应被丢弃，得到 count=%d", after.Count)
	}
}

func TestThroughputRingRate(t *testing.T) {
	var r throughputRing
	base := time.Now()
	for i := 0; i < 11; i++ {
		r.push(base.Add(time.Duration(i)*time.Second), int64(i*100), int64(i*1000))
	}
	up1, down1 := r.rate(time.Second)
	if up1 != 100 || down1 != 1000 {
		t.Fatalf("1 秒窗口速率期望 100/1000，得到 %v/%v", up1, down1)
	}
	up10, down10 := r.rate(10 * time.Second)
	if up10 != 100 || down10 != 1000 {
		t.Fatalf("10 秒窗口速率期望 100/1000，得到 %v/%v", up10, down10)
	}
	var empty throughputRing
	if u, d := empty.rate(time.Second); u != 0 || d != 0 {
		t.Fatalf("无Samples时应返回 0，得到 %v/%v", u, d)
	}
}

// ttyd 走 WebSocket，反代在 101 之后只是裸字节对拷——只有在 net.Conn 这层统计才看得到
// Terminal流量。这条用例把真实请求打穿反代，确认拨号采样与双向字节计数都动了。
func TestNetmonCountsProxiedTrafficAndHandshake(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(bytes.Repeat([]byte("x"), 4096))
	}))
	defer backend.Close()
	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	mon := newNetMonitor(target, 0, "operator@example.com")
	proxy := newTTYDProxy(target, mon)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), proxyUserKey{}, "operator@example.com"))
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("反代应 200，得到 %d", w.Code)
	}

	if mon.upBytes.Load() <= 0 {
		t.Fatal("门禁 → Terminal host方向的字节数没统计到")
	}
	if mon.downBytes.Load() < 4096 {
		t.Fatalf("Terminal host → 门禁方向至少应有 4096 字节，得到 %d", mon.downBytes.Load())
	}
	if mon.connOpened.Load() != 1 {
		t.Fatalf("应记录 1 次建连，得到 %d", mon.connOpened.Load())
	}
	if mon.dialRTT.stats().Count != 1 {
		t.Fatal("真实建连应顺带采一次握手时延Samples")
	}

	snap := mon.snapshot(3)
	if snap.Upstream.Target != target.Host {
		t.Fatalf("快照里的Terminal host期望 %s，得到 %s", target.Host, snap.Upstream.Target)
	}
	if snap.Upstream.Throughput.DownBytes < 4096 {
		t.Fatalf("快照下行累计字节不对：%d", snap.Upstream.Throughput.DownBytes)
	}
	if snap.Gate.ActiveTabs != 3 {
		t.Fatalf("快照应带上Terminal tabs数，得到 %d", snap.Gate.ActiveTabs)
	}
}

// 上游 ttyd 用 X-Webauth-User 鉴权，探测不带身份只会拿到 407——量到的是「被拒绝」的耗时，
// 走不到真实处理路径。这条用例守住探测必须带白名单身份。
func TestNetmonProbeCarriesTrustedIdentity(t *testing.T) {
	seen := make(chan string, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case seen <- r.Header.Get(authHeaderName):
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	target, _ := url.Parse(backend.URL)
	mon := newNetMonitor(target, 0, "operator@example.com")
	mon.probeOnce(context.Background())

	if got := <-seen; got != "operator@example.com" {
		t.Fatalf("探测应带上白名单身份头，得到 %q", got)
	}
	snap := mon.snapshot(0)
	if snap.Upstream.Probe.LastStatus != http.StatusOK {
		t.Fatalf("期望探测拿到 200，得到 %d", snap.Upstream.Probe.LastStatus)
	}
	if snap.Upstream.Probe.OKCount != 1 || snap.Upstream.Probe.FailCount != 0 {
		t.Fatalf("探测计数不对: %+v", snap.Upstream.Probe)
	}
	if snap.Upstream.Probe.TCP.Count != 1 || snap.Upstream.Probe.TTFB.Count != 1 {
		t.Fatal("探测应同时采到 TCP 建连与首字节两段耗时，只有分开才知道慢在Network还是慢在对端")
	}
}

func TestConfigProbeIdentityIsStable(t *testing.T) {
	cfg := Config{AllowedEmails: map[string]struct{}{
		"zoe@example.com": {}, "operator@example.com": {}, "amy@example.com": {},
	}}
	for i := 0; i < 20; i++ {
		if got := cfg.probeIdentity(); got != "amy@example.com" {
			t.Fatalf("探测身份必须每次一致（否则重启后统计口径会变），得到 %q", got)
		}
	}
	if got := (Config{}).probeIdentity(); got != "" {
		t.Fatalf("白名单为空时应返回空串，得到 %q", got)
	}
}

func TestNetmonRecordsProxyFailure(t *testing.T) {
	unreachable, _ := url.Parse("http://127.0.0.1:1")
	mon := newNetMonitor(unreachable, 0, "")
	proxy := newTTYDProxy(unreachable, mon)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("上游不可达应 502，得到 %d", w.Code)
	}
	if mon.proxyFails.Load() != 1 {
		t.Fatalf("应记录 1 次转发失败，得到 %d", mon.proxyFails.Load())
	}
	if mon.dialFails.Load() != 1 {
		t.Fatalf("应记录 1 次建连失败，得到 %d", mon.dialFails.Load())
	}
}

// 反代在单向拷贝结束后靠 CloseWrite 做 TCP 半Close；计数包装层吞掉它，上游就收不到 EOF。
func TestCountingConnForwardsCloseWrite(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("Close测试监听失败: %v", err)
		}
	})
	done := make(chan []byte, 1)
	go func() {
		c, aerr := ln.Accept()
		if aerr != nil {
			done <- nil
			return
		}
		defer func() {
			if err := c.Close(); err != nil {
				t.Errorf("Close测试Connection failed: %v", err)
			}
		}()
		b, _ := io.ReadAll(c) // 只有对端半Close写方向才会读到 EOF
		done <- b
	}()

	mon := newNetMonitor(nil, 0, "")
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	conn := &countingConn{Conn: raw, mon: mon}
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := conn.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite 应透传给底层 TCP 连接: %v", err)
	}
	select {
	case got := <-done:
		if string(got) != "hello" {
			t.Fatalf("对端期望读到 hello，得到 %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("对端没读到 EOF，说明 CloseWrite 被计数包装层吞掉了")
	}
	if mon.upBytes.Load() != 5 {
		t.Fatalf("上行字节应为 5，得到 %d", mon.upBytes.Load())
	}
	_ = conn.Close()
	if mon.connActive.Load() != -1 {
		// newNetMonitor 没经过 dialContext，所以 active 从 0 减到 -1；这里只验证 Close 幂等。
		t.Fatalf("Close 应恰好扣一次Active connections，得到 %d", mon.connActive.Load())
	}
	_ = conn.Close()
	if mon.connClosed.Load() != 1 {
		t.Fatalf("重复 Close 不应重复计数，得到 %d", mon.connClosed.Load())
	}
}

func TestNetmonEndpointsRequireSession(t *testing.T) {
	s := newTestServer(t)
	for _, tc := range []struct {
		name    string
		method  string
		path    string
		body    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{"快照", http.MethodGet, "/api/netmon", "", s.handleNetmon},
		{"探针", http.MethodGet, "/api/netmon/ping", "", s.handleNetmonPing},
		{"上报", http.MethodPost, "/api/netmon/sample", `{"clientId":"x"}`, s.handleNetmonSample},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		w := httptest.NewRecorder()
		tc.handler(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s Not signed in应 401，得到 %d", tc.name, w.Code)
		}
	}
}

func TestNetmonPingReturnsServerTime(t *testing.T) {
	s := newTestServer(t)
	token := signToken(t, "operator@example.com")
	req := withSession(httptest.NewRequest(http.MethodGet, "/api/netmon/ping", nil), token)
	w := httptest.NewRecorder()
	s.handleNetmonPing(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d", w.Code)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("探针必须禁缓存，否则量到的是缓存命中而不是往返时延")
	}
	var body map[string]int64
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if body["serverTimeMs"] <= 0 {
		t.Fatalf("期望返回服务端时间，得到 %v", body)
	}
}

func TestNetmonSampleRoundTrip(t *testing.T) {
	s := newTestServer(t)
	token := signToken(t, "operator@example.com")
	sample := `{"clientId":"page-1","device":"mobile","online":true,
		"gateRtt":{"count":10,"p50Ms":42.5,"p95Ms":88},
		"echoRtt":{"count":6,"p50Ms":130.5},
		"tabs":[{"tabId":"t1","title":"example-tui","connected":true,"downBps":2048,"upBps":16}]}`
	req := withSession(httptest.NewRequest(http.MethodPost, "/api/netmon/sample", strings.NewReader(sample)), token)
	w := httptest.NewRecorder()
	s.handleNetmonSample(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("上报应 200，得到 %d %s", w.Code, w.Body.String())
	}

	req2 := withSession(httptest.NewRequest(http.MethodGet, "/api/netmon", nil), token)
	w2 := httptest.NewRecorder()
	s.handleNetmon(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("快照应 200，得到 %d", w2.Code)
	}
	var snap netmonSnapshot
	if err := json.Unmarshal(w2.Body.Bytes(), &snap); err != nil {
		t.Fatalf("快照不是合法 JSON: %v", err)
	}
	if len(snap.Clients) != 1 {
		t.Fatalf("快照应含 1 份浏览器上报，得到 %d", len(snap.Clients))
	}
	c := snap.Clients[0]
	if c.ClientID != "page-1" || c.GateRTT.P50Ms != 42.5 || c.EchoRTT.P50Ms != 130.5 {
		t.Fatalf("浏览器上报没原样保留: %+v", c)
	}
	if len(c.Tabs) != 1 || c.Tabs[0].Title != "example-tui" {
		t.Fatalf("每个Terminal tabs的观测应保留: %+v", c.Tabs)
	}
	if c.AgeMs < 0 {
		t.Fatalf("应带上「多久之前上报的」，得到 %d", c.AgeMs)
	}
}

func TestNetmonSampleRejectsMissingClientID(t *testing.T) {
	s := newTestServer(t)
	token := signToken(t, "operator@example.com")
	req := withSession(httptest.NewRequest(http.MethodPost, "/api/netmon/sample", strings.NewReader(`{"device":"mobile"}`)), token)
	w := httptest.NewRecorder()
	s.handleNetmonSample(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺少 clientId 应 400，得到 %d", w.Code)
	}
}

func TestNetmonEvictsStaleAndExcessClients(t *testing.T) {
	mon := newNetMonitor(nil, 0, "")
	for i := 0; i < maxNetmonClients+3; i++ {
		mon.putClientSample(netmonClientSample{ClientID: "c" + strconv.Itoa(i)})
	}
	if got := len(mon.snapshot(0).Clients); got != maxNetmonClients {
		t.Fatalf("Online页面数应封顶 %d，得到 %d", maxNetmonClients, got)
	}
	// 过期的上报不该继续出现在快照里
	mon.mu.Lock()
	for _, c := range mon.clients {
		c.at = time.Now().Add(-2 * netmonClientTTL)
	}
	mon.mu.Unlock()
	if got := len(mon.snapshot(0).Clients); got != 0 {
		t.Fatalf("过期上报应被清掉，得到 %d", got)
	}
}

func TestNetmonSampleGoesToPerfLog(t *testing.T) {
	s := newTestServer(t)
	token := signToken(t, "operator@example.com")
	req := withSession(httptest.NewRequest(http.MethodPost, "/__shellgate/debug-log",
		strings.NewReader(`{"event":"netmon-sample","detail":{"gateRttP50":42}}`)), token)
	w := httptest.NewRecorder()
	s.handleDebugLog(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d", w.Code)
	}
	if _, err := os.Stat(filepath.Join(s.cfg.DataDir, "debug-log.ndjson")); !os.IsNotExist(err) {
		t.Fatal("netmon-sample 属于采样类，不应污染 debug-log.ndjson")
	}
	if _, err := os.Stat(filepath.Join(s.cfg.DataDir, "perf-log.ndjson")); err != nil {
		t.Fatalf("netmon-sample 应写入 perf-log.ndjson: %v", err)
	}
}

func TestTabStoreCountFor(t *testing.T) {
	ts := testTabDB(t)
	mustCreateTab(t, ts, "operator@example.com", "default", "a", "")
	mustCreateTab(t, ts, "operator@example.com", "default", "b", "example-tui")
	mustCreateTab(t, ts, "other@example.com", "default", "c", "")
	if got, err := ts.CountFor("OPERATOR@example.com"); got != 2 || err != nil {
		t.Fatalf("应只数自己的窗口（且不区分邮箱大小写），得到 %d", got)
	}
}

func TestWorkspaceHTMLHasNetmonEntryAndResponsiveTopBar(t *testing.T) {
	s := newTestServer(t)
	token := signToken(t, "operator@example.com")
	req := withSession(httptest.NewRequest(http.MethodGet, "/", nil), token)
	w := httptest.NewRecorder()
	s.handleRoot(w, req)
	body := w.Body.String()

	for _, needle := range []string{`id="btnNet"`, `id="netCard"`, "/api/netmon/ping", "/api/netmon/sample"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("工作台缺少Network diagnostics入口/采样 %q", needle)
		}
	}
	// 监控小窗必须是常驻悬浮的，不能做成弹窗：开着的时候Terminal要照常能用。
	if !strings.Contains(body, "class=\"netcard\"") {
		t.Fatal("Network diagnostics应是悬浮小窗，不是模态弹窗")
	}
	if strings.Contains(body, `data-close="netOverlay"`) {
		t.Fatal("Network diagnostics不得再走模态弹窗那套遮罩Close")
	}
	if !strings.Contains(body, ".netcard {\n      position: fixed;") {
		t.Fatal("监控小窗应固定悬浮，不占据文档流")
	}
	if !strings.Contains(body, "max-height: min(28rem,") {
		t.Fatal("监控小窗必须限制高度，不能大面积遮住Terminal")
	}
	// 常驻小窗一夺焦点，手机上软键盘就收了。
	if !strings.Contains(body, `getElementById("netCardHead").addEventListener("pointerdown"`) {
		t.Fatal("监控小窗标题栏必须不夺Terminal焦点")
	}
	if !strings.Contains(body, `getElementById("btnNetCollapse").textContent = "－"`) {
		t.Fatal("监控小窗重新打开时必须同步展开图标")
	}
	// 窄屏收纳：左右两个菜单入口 + 媒体查询隐藏平铺按钮，否则手机上顶栏挤爆。
	for _, needle := range []string{`id="btnLaunchMenu"`, `id="btnMoreMenu"`, `id="launchMenu"`, `id="moreMenu"`, "@media (max-width: 760px)"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("工作台缺少窄屏顶栏收纳 %q", needle)
		}
	}
	if !strings.Contains(body, ".machines, .launchers, .rail { display: none; }") {
		t.Fatal("窄屏下必须隐藏平铺的Machine/Launch器/工具轨，改走左右菜单")
	}
	// 单行顶栏：Tab 横向滚动时不得把 overflow-y 带成 auto，否则滚动条会把顶栏撑高Jitter。
	if !strings.Contains(body, "overflow-y: hidden") {
		t.Fatal("顶栏 Tab 区必须 overflow-y:hidden，防止新开 Tab 后高度Jitter")
	}
	if !strings.Contains(body, "flex: 1 1 0%") {
		t.Fatal("顶栏 Tab 区须用 flex-basis 0%，才能把余量让给横向滚动而不是撑高整行")
	}
	// 菜单遮罩不可少：点Terminal iframe 的事件不会冒泡到父页，没有它菜单关不掉。
	if !strings.Contains(body, `id="menuScrim"`) {
		t.Fatal("菜单缺少遮罩层，点 iframe 时无法收起菜单")
	}
	if !strings.Contains(body, `/api/machines/`) || !strings.Contains(body, `/ready`) ||
		!strings.Contains(body, `waking it if asleep`) {
		t.Fatal("工作台缺少 Mac 按需唤醒入口或等待Status")
	}
	// 菜单必须在 overflow:hidden 的 .top 行外：行内裁切会让窄屏 ☰/⋯ 下拉看不见也点不到。
	if !strings.Contains(body, `id="topShell"`) || !strings.Contains(body, ".top-shell.menu-open") {
		t.Fatal("窄屏菜单须挂在 .top-shell 上抬层，不能只靠 .top")
	}
	if strings.Contains(body, ".top.menu-open") {
		t.Fatal("menu-open 不得再挂在 overflow:hidden 的 .top 上")
	}
	topIdx := strings.Index(body, `<header class="top">`)
	topEnd := strings.Index(body[topIdx:], `</header>`)
	if topIdx < 0 || topEnd < 0 {
		t.Fatal("工作台缺少顶栏 header")
	}
	headerChunk := body[topIdx : topIdx+topEnd]
	if strings.Contains(headerChunk, `id="launchMenu"`) || strings.Contains(headerChunk, `id="moreMenu"`) {
		t.Fatal("launchMenu/moreMenu 不得放在 overflow:hidden 的 .top 内，否则会被裁掉")
	}
	if !strings.Contains(body, "e.origin !== location.origin") {
		t.Fatal("接收 iframe 上报必须校验同源，否则任意页面都能伪造监控数据")
	}
	// 多机器：切换机器 pill 必须过滤 Tab 集合，其它机器的会话后台保活；
	// 切回去优先恢复该机上次激活的 Tab，而不是一律落到最近一个。
	for _, needle := range []string{
		"function tabsOnMachine",
		"tabsOnMachine(selectedMachine)",
		"切换机器 = 切换 Tab 集合",
		"lastTabOnMachine",
		"activeByMachine",
		"rememberedTabOnMachine",
		"优先该机上次激活的 Tab",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("工作台缺少「切换Machine跟 Tab 集合」%q", needle)
		}
	}
	if strings.Contains(body, "machdot") {
		t.Fatal("Tab 条已按Machine过滤后不应再渲染Machine色点徽标")
	}
}

func TestMacTabLeaseIsIndependentOfTTYDWebSocket(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "deploy", "mac", "terminal-shell"))
	if err != nil {
		t.Fatalf("读取 Mac ttyd 登录脚本失败: %v", err)
	}
	script := string(body)
	if strings.Contains(script, "caffeinate") {
		t.Fatal("ttyd 的 WebSocket 登录脚本不得持有睡眠断言")
	}
	lease, err := os.ReadFile(filepath.Join("..", "..", "deploy", "mac", "shell-gate-session-lease"))
	if err != nil {
		t.Fatalf("读取 Mac Tab 租约脚本失败: %v", err)
	}
	if !strings.Contains(string(lease), "caffeinate -is -t \"$lease_seconds\"") ||
		!strings.Contains(string(lease), "readonly lease_seconds=28800") {
		t.Fatal("Mac Tab 必须由有 8 小时兜底的独立租约保持唤醒")
	}
}

// Mac LaunchAgent/ttyd 常缺登录环境：登录脚本必须补齐账号默认壳与真彩声明，
// 否则门禁的 `"$SHELL" -lic` 会进错壳，TUI 也会被压成 256 色。
func TestMacTabShellExportsLoginShellAndTruecolor(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "deploy", "mac", "terminal-shell"))
	if err != nil {
		t.Fatalf("读取 Mac ttyd 登录脚本失败: %v", err)
	}
	script := string(body)
	for _, needle := range []string{
		`export SHELL=`,
		`COLORTERM="${COLORTERM:-truecolor}"`,
		`UserShell`,
		`exec "${SHELL}" -li`,
		`exec bash -lic`,
		`exec "$SHELL" -lic`,
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("Mac 登录脚本缺少 %q（真彩/默认壳兜底）", needle)
		}
	}
}

func TestEnhanceJSMapsCommandToCtrl(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/__shellgate/enhance.js", nil)
	w := httptest.NewRecorder()
	s.handleEnhanceJS(w, req)
	body := w.Body.String()
	for _, needle := range []string{"installCommandAsCtrl", "isAppleClient", "cmd-as-ctrl"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("enhance.js 缺少 Command→Ctrl 映射 %q", needle)
		}
	}
	// C / V 必须留给剪贴板：Command+C / Command+V 是本页CopyPaste的唯一入口，
	// 一旦被映射成控制字符，CopyPaste（含图片Paste哨兵）会整个失效。
	if !strings.Contains(body, `key === "c" || key === "v"`) {
		t.Fatal("Command→Ctrl 映射必须放行 C / V，否则CopyPaste通道被抢走")
	}
	// 只在按下 Command 且没有其它修饰键时接管，避免抢走 Cmd+Shift+V 一类的浏览器组合。
	if !strings.Contains(body, "!e.metaKey || e.ctrlKey || e.altKey || e.shiftKey") {
		t.Fatal("Command→Ctrl 映射必须放行带 Ctrl / Alt / Shift 的组合")
	}
}

func TestEnhanceJSMeasuresEchoLatencyAndReportsToParent(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/__shellgate/enhance.js", nil)
	w := httptest.NewRecorder()
	s.handleEnhanceJS(w, req)
	body := w.Body.String()
	for _, needle := range []string{"createRttTracker", "installNetmonBridge", "postMessage", "totalDownBytes", "lastCloseCode"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("enhance.js 缺少Network diagnostics %q", needle)
		}
	}
	// 回显计时只认 ttyd 的输入帧（Command码 '0'），否则改窗口大小等控制帧会被当成按键。
	// 而 ttyd 1.7.x 是用 TextEncoder 把Command编码成 Uint8Array 再发的，**不是字符串**：
	// 只判字符串会一条输入帧都认不出来，Echo samples恒为 0（2026-08-03 真机实测）。
	if !strings.Contains(body, "function ttydInputFrameSize") {
		t.Fatal("输入帧识别应独立成函数，并同时支持字符串与二进制两种形态")
	}
	for _, needle := range []string{"bytes[0] === 0x30", "ArrayBuffer.isView", "data instanceof ArrayBuffer"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("输入帧识别缺少二进制分支 %q，ttyd 发的是 Uint8Array 而不是字符串", needle)
		}
	}
	if !strings.Contains(body, "ttydInputFrameSize(data)") {
		t.Fatal("回显计时必须走输入帧识别，不能自己判字符串首字符")
	}
	if !strings.Contains(body, "stats.echo.add(dt)") {
		t.Fatal("缺少按键→回显往返的落Samples逻辑")
	}
	// 累计计数不能被 5s 的卡顿采样清零，否则速率算出来忽大忽小。
	idx := strings.Index(body, "function installPerfSampler")
	if idx < 0 {
		t.Fatal("缺少 installPerfSampler")
	}
	seg := body[idx:]
	if end := strings.Index(seg, "\n  // ---------- Network diagnostics"); end > 0 {
		seg = seg[:end]
	}
	for _, bad := range []string{"stats.totalDownBytes = 0", "stats.totalUpBytes = 0"} {
		if strings.Contains(seg, bad) {
			t.Fatalf("卡顿采样不得清零累计计数 %q", bad)
		}
	}
}

func TestEnhanceJSSelfRepairsDisplayCorruption(t *testing.T) {
	// 显示自愈是 WebGL 被重新打开时的双保险，不是产品路径（默认已是 canvas）。
	// 守住：手动入口、自动触发、真正调到重建接口——禁止为了「默认不用」把代码删掉。
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/__shellgate/enhance.js", nil)
	w := httptest.NewRecorder()
	s.handleEnhanceJS(w, req)
	js := w.Body.String()

	for _, needle := range []string{
		"repair-display",                     // 接收父页「Repair display」广播
		"term.clearTextureAtlas()",           // 真正调用官方贴图重建接口
		"installDisplayRepair",               // 自动触发与快捷键的安装入口
		"installFullRedrawRepair",            // 使用中整屏重绘这条主路径
		`d.__shellgate === "repair-display"`, // 广播协议字段
	} {
		if !strings.Contains(js, needle) {
			t.Fatalf("enhance.js 缺少显示自愈实现 %q", needle)
		}
	}
	// 自动触发：睡眠唤醒/切后台、缩放/跨屏、以及 TUI 使用中的整屏重绘爆发。
	for _, needle := range []string{
		"visibilitychange",
		"matchMedia(\"(resolution: ",
		"full-redraw",
		"isFullViewportRedraw",
	} {
		if !strings.Contains(js, needle) {
			t.Fatalf("enhance.js 缺少显示自愈自动触发 %q", needle)
		}
	}

	// 父页「Repair display」菜单入口与广播必须同时在，只改一侧等于入口断了。
	token := signToken(t, "operator@example.com")
	req2 := withSession(httptest.NewRequest(http.MethodGet, "/", nil), token)
	w2 := httptest.NewRecorder()
	s.handleRoot(w2, req2)
	page := w2.Body.String()
	for _, needle := range []string{`data-act="repair"`, "Repair display", "repair-display"} {
		if !strings.Contains(page, needle) {
			t.Fatalf("工作台 HTML 缺少显示修复入口 %q", needle)
		}
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

// 回显计时口径：Terminal能力查询帧不得被当成按键回显。
//
// 背景（2026-08-05）：全屏 TUI 会定时向Terminal发查询——实测空闲时每 2.00 秒发一帧
// `\x1b[?996n` + `\x1b]11;?`。而 TUI 里大量按键根本不触发重绘（实测 30 次按键 23 次无回显），
// 计时器就一路等到那个查询帧并记成「回显」，Samples值可达 2000ms，把 P95 顶到 500ms+，
// 而用户实际打字并不卡。正则逻辑 Go 侧断言不出对错，交给 node 真跑一遍。
func TestEnhanceQueryFrameFilter(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("本机没有 node，跳过回显计时口径的守护测试")
	}
	out, err := exec.Command(node, filepath.Join("web", "enhance_query_frame_test.mjs")).CombinedOutput()
	if err != nil {
		t.Fatalf("回显计时口径守护测试未通过: %v\n%s", err, out)
	}
}

// ---------- 多上游Machine支持 ----------

func TestParseMachines(t *testing.T) {
	// 多目标：顺序保留、Name地址都解析
	got, err := parseMachines("linux=http://203.0.113.2:7681, tokyo=http://203.0.113.9:7681", "")
	if err != nil {
		t.Fatalf("合法列表不应报错: %v", err)
	}
	if len(got) != 2 || got[0].Name != "linux" || got[1].Name != "tokyo" {
		// 上面故意写了 tokyyo？没有--Name原样保留，这里断言的是原样解析
		t.Fatalf("Machine列表解析不对: %+v", got)
	}
	_ = got

	// 单值回退：Machine名 default，单机部署升级零改动
	one, err := parseMachines("", "http://203.0.113.2:7681")
	if err != nil {
		t.Fatalf("单值回退不应报错: %v", err)
	}
	if len(one) != 1 || one[0].Name != "default" || one[0].URL.Host != "203.0.113.2:7681" {
		t.Fatalf("单值回退解析不对: %+v", one)
	}

	// 重名 / 非法名 / 非法地址 / 都没配：fail-fast
	if _, err := parseMachines("a=http://x:1,a=http://x:2", ""); err == nil {
		t.Fatal("重复Machine名应报错")
	}
	if _, err := parseMachines("bad name=http://x:1", ""); err == nil {
		t.Fatal("带空格的Machine名应报错")
	}
	if _, err := parseMachines("a=notaurl", ""); err == nil {
		t.Fatal("非法地址应报错")
	}
	if _, err := parseMachines("", ""); err == nil {
		t.Fatal("一个都没配应报错")
	}
	// 非 http(s)、内嵌账号、缺主机：一律拒绝，避免反代信任边界被配歪
	if _, err := parseMachines("a=file:///tmp/x", ""); err == nil {
		t.Fatal("file:// 应报错")
	}
	if _, err := parseMachines("a=ftp://203.0.113.2:21", ""); err == nil {
		t.Fatal("ftp:// 应报错")
	}
	if _, err := parseMachines("a=http://user:pass@203.0.113.2:7681", ""); err == nil {
		t.Fatal("含 userinfo 的上游地址应报错")
	}
	httpsOne, err := parseMachines("", "HTTPS://203.0.113.2:7681")
	if err != nil {
		t.Fatalf("HTTPS 大写 scheme 应接受: %v", err)
	}
	if httpsOne[0].URL.Scheme != "https" {
		t.Fatalf("scheme 应规范化为小写 https，得到 %q", httpsOne[0].URL.Scheme)
	}
}

func TestAttachWakeCommands(t *testing.T) {
	u, _ := url.Parse("http://203.0.113.4:7681")
	machines := []MachineConfig{{Name: "mac", URL: u}}

	hook := filepath.Join(t.TempDir(), "wake-mac")
	got, err := attachWakeCommands(machines, "mac="+hook)
	if err != nil {
		t.Fatalf("合法唤醒配置不应报错: %v", err)
	}
	if got[0].WakeCommand != hook {
		t.Fatalf("唤醒Command未绑定: %+v", got[0])
	}
	if machines[0].WakeCommand != "" {
		t.Fatal("解析不得修改调用方传入的Machine切片")
	}

	invalid := []struct {
		name string
		raw  string
	}{
		{name: "未知Machine", raw: "linux=/wake"},
		{name: "相对路径", raw: "mac=bin/wake"},
		{name: "重复Machine", raw: "mac=/wake,mac=/wake2"},
		{name: "缺少路径", raw: "mac="},
		{name: "只有分隔符", raw: ", ,"},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := attachWakeCommands(machines, tt.raw); err == nil {
				t.Fatalf("非法配置 %q 应报错", tt.raw)
			}
		})
	}

	dir := t.TempDir()
	executable := filepath.Join(dir, "wake")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := validateWakeCommands([]MachineConfig{{Name: "mac", WakeCommand: executable}}); err != nil {
		t.Fatalf("存在且可执行的唤醒入口应通过校验: %v", err)
	}
	notExecutable := filepath.Join(dir, "not-executable")
	if err := os.WriteFile(notExecutable, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateWakeCommands([]MachineConfig{{Name: "mac", WakeCommand: notExecutable}}); err == nil {
		t.Fatal("不可执行的唤醒入口应Launch失败")
	}
	if err := validateWakeCommands([]MachineConfig{{Name: "mac", WakeCommand: filepath.Join(dir, "missing")}}); err == nil {
		t.Fatal("不存在的唤醒入口应Launch失败")
	}
}

func TestParseSessionAndNetmonDurations(t *testing.T) {
	t.Setenv(envSessionTTL, "")
	t.Setenv(envNetmonInterval, "")
	d, err := parsePositiveDuration(envSessionTTL, defaultSessionTTL)
	if err != nil || d != defaultSessionTTL {
		t.Fatalf("空会话 TTL 应回落默认: d=%v err=%v", d, err)
	}
	t.Setenv(envSessionTTL, "0")
	if _, err := parsePositiveDuration(envSessionTTL, defaultSessionTTL); err == nil {
		t.Fatal("会话 TTL=0 应报错")
	}
	t.Setenv(envSessionTTL, "-1h")
	if _, err := parsePositiveDuration(envSessionTTL, defaultSessionTTL); err == nil {
		t.Fatal("负会话 TTL 应报错")
	}
	t.Setenv(envSessionTTL, "not-a-duration")
	if _, err := parsePositiveDuration(envSessionTTL, defaultSessionTTL); err == nil {
		t.Fatal("非法会话 TTL 应报错，不得静默默认")
	}
	t.Setenv(envSessionTTL, "90m")
	d, err = parsePositiveDuration(envSessionTTL, defaultSessionTTL)
	if err != nil || d != 90*time.Minute {
		t.Fatalf("合法会话 TTL 解析失败: d=%v err=%v", d, err)
	}

	t.Setenv(envNetmonInterval, "0")
	n, err := parseNonNegativeDuration(envNetmonInterval, defaultNetmonInterval)
	if err != nil || n != 0 {
		t.Fatalf("探测间隔 0 应允许: n=%v err=%v", n, err)
	}
	t.Setenv(envNetmonInterval, "-5s")
	if _, err := parseNonNegativeDuration(envNetmonInterval, defaultNetmonInterval); err == nil {
		t.Fatal("负探测间隔应报错")
	}
}

func TestTTYDProxyTransportDoesNotUseEnvProxy(t *testing.T) {
	s := newTestServer(t)
	tr, ok := s.primaryUpstream().proxy.(*httputil.ReverseProxy).Transport.(*http.Transport)
	if !ok {
		t.Fatal("反代 Transport 应为 *http.Transport")
	}
	if tr.Proxy != nil {
		t.Fatal("上游反代不得使用环境 HTTP(S)_PROXY，避免身份头与Terminal流量外泄")
	}
}

func TestMachinesEndpoint(t *testing.T) {
	s := newTestServer(t)
	s.primaryUpstream().waker = newUpstreamWaker("/wake", "127.0.0.1:7681")
	token := signToken(t, "operator@example.com")
	req := withSession(httptest.NewRequest(http.MethodGet, "/api/machines", nil), token)
	w := httptest.NewRecorder()
	s.handleMachines(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d", w.Code)
	}
	var body struct {
		Machines []machineView `json:"machines"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if len(body.Machines) != 1 {
		t.Fatalf("应返回 1 台Machine，得到 %d", len(body.Machines))
	}
	m := body.Machines[0]
	if !m.Default || !m.Healthy || !m.Wakeable || m.Name != "default" {
		t.Fatalf("默认Machine字段不对: %+v", m)
	}
	// Not signed in必须 401：Machine目录虽是展示数据，但也是内网拓扑信息
	req2 := httptest.NewRequest(http.MethodGet, "/api/machines", nil)
	w2 := httptest.NewRecorder()
	s.handleMachines(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("Not signed in应 401，得到 %d", w2.Code)
	}
}

func TestMachineReadyEndpointSuppressesWakeWhenAtHome(t *testing.T) {
	s := newTestServer(t)
	u := s.primaryUpstream()
	u.waker = newUpstreamWaker("/wake", u.target.Host)
	u.waker.homeCheck = &stubHomePresence{atHome: true}
	u.waker.probe = func(context.Context, string) error { return errors.New("offline") }
	u.waker.run = func(context.Context, string) error {
		t.Fatal("在家时不应唤醒")
		return nil
	}

	token := signToken(t, "operator@example.com")
	req := withSession(httptest.NewRequest(http.MethodPost, "/api/machines/default/ready", nil), token)
	req.SetPathValue("name", "default")
	w := httptest.NewRecorder()
	s.handleMachineReady(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("在家抑制应 503，得到 %d：%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "在家") {
		t.Fatalf("响应应含在家提示: %s", w.Body.String())
	}
}

func TestMachineReadyEndpointWakesOfflineUpstream(t *testing.T) {
	s := newTestServer(t)
	u := s.primaryUpstream()
	runs := 0
	u.waker = newUpstreamWaker("/wake", u.target.Host)
	u.waker.probe = func(context.Context, string) error {
		if runs > 0 {
			return nil
		}
		return errors.New("offline")
	}
	u.waker.run = func(context.Context, string) error {
		runs++
		return nil
	}

	token := signToken(t, "operator@example.com")
	req := withSession(httptest.NewRequest(http.MethodPost, "/api/machines/default/ready", nil), token)
	req.SetPathValue("name", "default")
	w := httptest.NewRecorder()
	s.handleMachineReady(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("自动唤醒应返回 200，得到 %d：%s", w.Code, w.Body.String())
	}
	if runs != 1 {
		t.Fatalf("唤醒Command执行 %d 次，期望 1 次", runs)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/machines/default/ready", nil)
	req.SetPathValue("name", "default")
	w = httptest.NewRecorder()
	s.handleMachineReady(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("Not signed in唤醒应返回 401，得到 %d", w.Code)
	}
}

func TestCreateTabWakesConfiguredUpstream(t *testing.T) {
	s := newTestServer(t)
	u := s.primaryUpstream()
	runs := 0
	u.waker = newUpstreamWaker("/wake", u.target.Host)
	u.waker.probe = func(context.Context, string) error {
		if runs > 0 {
			return nil
		}
		return errors.New("offline")
	}
	u.waker.run = func(context.Context, string) error {
		runs++
		return nil
	}

	token := signToken(t, "operator@example.com")
	req := withSession(httptest.NewRequest(http.MethodPost, "/api/tabs", strings.NewReader(`{"title":"shell"}`)), token)
	w := httptest.NewRecorder()
	s.handleCreateTab(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("创建 Tab 应自动唤醒并返回 200，得到 %d：%s", w.Code, w.Body.String())
	}
	if runs != 1 {
		t.Fatalf("创建 Tab 应执行一次唤醒，实际 %d 次", runs)
	}
}

func TestMacTabLeaseSurvivesDisconnectUntilExplicitClose(t *testing.T) {
	s := newTestServer(t)
	u := s.primaryUpstream()
	var actions []string
	u.leaser = newSessionLeaser("/lease")
	u.leaser.run = func(_ context.Context, _ string, action, tabID string) error {
		actions = append(actions, action+":"+tabID)
		return nil
	}

	token := signToken(t, "operator@example.com")
	create := withSession(httptest.NewRequest(http.MethodPost, "/api/tabs", strings.NewReader(`{"title":"shell"}`)), token)
	created := httptest.NewRecorder()
	s.handleCreateTab(created, create)
	if created.Code != http.StatusOK {
		t.Fatalf("创建 Tab 应成功，得到 %d：%s", created.Code, created.Body.String())
	}
	if len(actions) != 1 || !strings.HasPrefix(actions[0], "acquire:") {
		t.Fatalf("创建 Tab 必须取得租约: %#v", actions)
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(created.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	// 浏览器断线或刷新不会请求Delete接口，因此服务端 Tab 与租约必须仍在。
	if _, exists := s.tabs.Get(body.ID); !exists || len(actions) != 1 {
		t.Fatalf("非显式Close不应释放租约: exists=%v actions=%#v", exists, actions)
	}

	deleteReq := withSession(httptest.NewRequest(http.MethodDelete, "/api/tabs/"+body.ID, nil), token)
	deleteReq.SetPathValue("id", body.ID)
	deleted := httptest.NewRecorder()
	s.handleDeleteTab(deleted, deleteReq)
	if deleted.Code != http.StatusOK {
		t.Fatalf("明确Close Tab 应成功，得到 %d：%s", deleted.Code, deleted.Body.String())
	}
	if len(actions) != 2 || actions[1] != "release:"+body.ID {
		t.Fatalf("明确Close必须释放同一租约: %#v", actions)
	}
}

func TestWorkspaceRefreshRestoresExistingTabsWithoutOpeningAnother(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("web", "workspace.html"))
	if err != nil {
		t.Fatalf("读取工作台页面失败: %v", err)
	}
	html := string(body)
	for _, want := range []string{
		`const TAB_STATE_KEY = "shellgate_tabs_state";`,
		`async function restoreTabsAfterRefresh()`,
		`const data = await api("/api/tabs");`,
		`tabs = (data.tabs || []).map((tab) => ({`,
		`if (!tabs.length) return openTab({ title: "shell" });`,
		`// 仅恢复本浏览器窗口此前打开的那组 Tab`,
	} {
		if strings.HasPrefix(want, "//") {
			if strings.Contains(html, want) {
				t.Fatalf("不得再按本窗口临时Status过滤账号 Tab: %q", want)
			}
			continue
		}
		if !strings.Contains(html, want) {
			t.Fatalf("账号级 Tab 恢复守卫缺失: %q", want)
		}
	}
}

func TestTabStoreSurvivesProcessReload(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tabs.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	first := mustTabStore(t, st)
	tab := mustCreateTab(t, first, "operator@example.com", "linux", "example-tui", "example-tui")
	_ = st.Close()

	st2, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	reloaded := mustTabStore(t, st2)
	got, ok := reloaded.Get(tab.ID)
	if !ok || got.Title != "example-tui" || got.Command != "example-tui" || got.Machine != "linux" {
		t.Fatalf("门禁重启后必须从库恢复同一 Tab: ok=%v got=%#v", ok, got)
	}
	listed, err := reloaded.ListFor("OPERATOR@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != tab.ID {
		t.Fatalf("按账号列出应仍有该 Tab: %#v", listed)
	}
}

func TestListTabsReturnsOnlyCurrentUsersExistingTabs(t *testing.T) {
	s := newTestServer(t)
	owned := mustCreateTab(t, s.tabs, "operator@example.com", "default", "mine", "")
	mustCreateTab(t, s.tabs, "other@example.com", "default", "other", "")
	token := signToken(t, "operator@example.com")
	req := withSession(httptest.NewRequest(http.MethodGet, "/api/tabs", nil), token)
	w := httptest.NewRecorder()
	s.handleListTabs(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("列出已有 Tab 应成功，得到 %d：%s", w.Code, w.Body.String())
	}
	var body struct {
		Tabs []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"tabs"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Tabs) != 1 || body.Tabs[0].ID != owned.ID || body.Tabs[0].Path != "/t/"+owned.ID+"/" {
		t.Fatalf("刷新恢复只能得到本账号存活 Tab: %#v", body.Tabs)
	}
}

func TestTermProxyDoesNotWakeHiddenTab(t *testing.T) {
	s := newTestServer(t)
	u := s.primaryUpstream()
	probes := 0
	runs := 0
	u.waker = newUpstreamWaker("/wake", u.target.Host)
	u.waker.probe = func(context.Context, string) error {
		probes++
		return errors.New("offline")
	}
	u.waker.run = func(context.Context, string) error {
		runs++
		return nil
	}

	tab := mustCreateTab(t, s.tabs, "operator@example.com", "default", "shell", "")
	req := httptest.NewRequest(http.MethodGet, "/t/"+tab.ID+"/", nil)
	w := httptest.NewRecorder()
	s.handleTermProxy(w, req, "operator@example.com")
	if probes != 0 || runs != 0 {
		t.Fatalf("隐藏 Tab 的反代重连不得唤醒Machine：probes=%d runs=%d", probes, runs)
	}
}

func TestCreateTabRoutesToMachine(t *testing.T) {
	// 两台Machine都指向同一个本地假后端，验证「Tab 开在哪台Machine上」由请求/Presets决定。
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	u, _ := url.Parse(backend.URL)

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	verifier, err := authkit.New(authkit.Config{
		Secret: []byte(testSecret), Issuers: []string{"https://auth.example.com"}, Audience: "authenticated",
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Listen:         "127.0.0.1:0",
		AllowedEmails:  map[string]struct{}{"operator@example.com": {}},
		SupabaseURL:    "https://auth.example.com",
		SupabaseAnon:   "anon",
		Machines:       []MachineConfig{{Name: "linux", URL: u}, {Name: "tokyo", URL: u}},
		CookieName:     "shell_session",
		SessionTTL:     time.Hour,
		DataDir:        dir,
		DatabaseURL:    filepath.Join(dir, "test.db"),
		NetmonInterval: 0,
	}
	s := &Server{
		cfg: cfg, verifier: verifier, sessionSecret: []byte(testSecret),
		sessionIssuer: "https://auth.example.com", sessionAudience: "authenticated",
		store: st, tabs: mustTabStore(t, st), ready: true,
	}
	s.initUpstreams()
	token := signToken(t, "operator@example.com")

	// 指定Machine：落在指定Machine上
	req := withSession(httptest.NewRequest(http.MethodPost, "/api/tabs",
		strings.NewReader(`{"title":"sh","machine":"tokyo"}`)), token)
	w := httptest.NewRecorder()
	s.handleCreateTab(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("开 Tab 应 200，得到 %d %s", w.Code, w.Body.String())
	}
	var tab struct {
		ID      string `json:"id"`
		Machine string `json:"machine"`
		Path    string `json:"path"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &tab)
	if tab.Machine != "tokyo" {
		t.Fatalf("Tab 应开在 tokyo 上，得到 %q", tab.Machine)
	}
	ts, _ := s.tabs.Get(tab.ID)
	if ts.Machine != "tokyo" {
		t.Fatalf("Tab 会话里的Machine不对: %+v", ts)
	}

	// 不指定Machine：落在默认Machine（列表第一台）
	req2 := withSession(httptest.NewRequest(http.MethodPost, "/api/tabs",
		strings.NewReader(`{"title":"sh"}`)), token)
	w2 := httptest.NewRecorder()
	s.handleCreateTab(w2, req2)
	var tab2 struct {
		Machine string `json:"machine"`
	}
	_ = json.Unmarshal(w2.Body.Bytes(), &tab2)
	if tab2.Machine != "linux" {
		t.Fatalf("未指定Machine应落在默认Machine linux，得到 %q", tab2.Machine)
	}

	// 不存在的Machine：400
	req3 := withSession(httptest.NewRequest(http.MethodPost, "/api/tabs",
		strings.NewReader(`{"title":"sh","machine":"nope"}`)), token)
	w3 := httptest.NewRecorder()
	s.handleCreateTab(w3, req3)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("未知Machine应 400，得到 %d", w3.Code)
	}

	// Presets绑定Machine：优先级高于请求指定
	p, err := st.Create("operator@example.com", "example-tui", "example-tui", "tokyo", 0)
	if err != nil {
		t.Fatal(err)
	}
	req4 := withSession(httptest.NewRequest(http.MethodPost, "/api/tabs",
		strings.NewReader(`{"presetId":"`+p.ID+`","machine":"linux"}`)), token)
	w4 := httptest.NewRecorder()
	s.handleCreateTab(w4, req4)
	var tab4 struct {
		Machine string `json:"machine"`
		Command string `json:"command"`
	}
	_ = json.Unmarshal(w4.Body.Bytes(), &tab4)
	if tab4.Machine != "tokyo" || tab4.Command != "example-tui" {
		t.Fatalf("Presets绑定的Machine应优先，得到 machine=%q command=%q", tab4.Machine, tab4.Command)
	}

	// Presets绑定的Machine不合法：创建Presets时拦下
	req5 := withSession(httptest.NewRequest(http.MethodPost, "/api/presets",
		strings.NewReader(`{"name":"x","command":"ls","machine":"ghost"}`)), token)
	w5 := httptest.NewRecorder()
	s.handleCreatePreset(w5, req5)
	if w5.Code != http.StatusBadRequest {
		t.Fatalf("绑定未知Machine的Presets应 400，得到 %d", w5.Code)
	}
}

func TestNetmonSnapshotSplitsUpstreams(t *testing.T) {
	u1, _ := url.Parse("http://203.0.113.2:7681")
	u2, _ := url.Parse("http://203.0.113.9:7681")
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := Config{
		Listen: "127.0.0.1:0", AllowedEmails: map[string]struct{}{"operator@example.com": {}},
		SupabaseURL: "https://auth.example.com", SupabaseAnon: "anon",
		Machines:   []MachineConfig{{Name: "linux", URL: u1}, {Name: "tokyo", URL: u2}},
		CookieName: "shell_session", SessionTTL: time.Hour,
		DataDir: dir, DatabaseURL: filepath.Join(dir, "t.db"), NetmonInterval: 0,
	}
	verifier, err := authkit.New(authkit.Config{
		Secret: []byte(testSecret), Issuers: []string{"https://auth.example.com"}, Audience: "authenticated",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: cfg, verifier: verifier, sessionSecret: []byte(testSecret),
		sessionIssuer: "https://auth.example.com", sessionAudience: "authenticated",
		store: st, tabs: mustTabStore(t, st), ready: true}
	s.initUpstreams()
	token := signToken(t, "operator@example.com")
	req := withSession(httptest.NewRequest(http.MethodGet, "/api/netmon", nil), token)
	w := httptest.NewRecorder()
	s.handleNetmon(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("快照应 200，得到 %d", w.Code)
	}
	var multi netmonMultiSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &multi); err != nil {
		t.Fatalf("快照不是合法 JSON: %v", err)
	}
	if len(multi.Upstreams) != 2 {
		t.Fatalf("应按Machine拆成 2 份上游快照，得到 %d", len(multi.Upstreams))
	}
	if multi.Upstreams[0].Name != "linux" || multi.Upstreams[1].Name != "tokyo" {
		t.Fatalf("Machine顺序应保持配置顺序: %+v", multi.Upstreams)
	}
	if multi.Upstreams[0].Target != "203.0.113.2:7681" || multi.Upstreams[1].Target != "203.0.113.9:7681" {
		t.Fatalf("各Machine的地址不对: %+v", multi.Upstreams)
	}
}
