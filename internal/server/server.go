package server

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	authkit "github.com/x0c/shell-gate/internal/auth"
	"github.com/x0c/shell-gate/internal/server/store"
)

//go:embed web/login.html
var loginHTML []byte

//go:embed web/edge-pick.html
var edgePickHTML []byte

//go:embed web/workspace.html
var workspaceHTML []byte

//go:embed web/vendor/external-auth.js
var externalAuthJS []byte

//go:embed web/enhance.js
var enhanceJS []byte

// enhanceScriptTag 注入进 ttyd 终端页面的增强脚本标签：中文全角标点修复、
// 截图/图片粘贴、剪贴板桥接、手机端键盘避让与快捷键栏、卡顿诊断采样。
// 见 web/enhance.js。
const enhanceScriptTag = `<script src="/__shellgate/enhance.js"></script>`

const authHeaderName = "X-Webauth-User"

// upstream 是一台可反代的终端主机（ttyd）：每台机器独立持有反代与网络监控，
// 互不影响--一台机器的探测超时不会拖慢另一台的转发。
type upstream struct {
	name   string
	target *url.URL
	proxy  TerminalBackend
	netmon *netMonitor
	waker  *upstreamWaker
	leaser *sessionLeaser
}

// Server 是通行密钥门禁 + 多 Tab 工作台 + ttyd 反代。
type Server struct {
	cfg             Config
	verifier        TokenVerifier
	sessionVerifier TokenVerifier
	passkeys        *localPasskeys
	passwords       *passwordGuard
	sessionSecret   []byte
	sessionIssuer   string
	sessionAudience string
	store           *store.Store
	tabs            *tabStore
	httpSrv         *http.Server
	upstreams       []*upstream // 顺序即配置顺序（展示顺序），第一台是默认机器
	upstreamByName  map[string]*upstream
	ready           bool
	mu              sync.RWMutex
	wg              sync.WaitGroup
}

// initUpstreams 按配置构建全部上游机器（每台一个反代 + 一个监控器）。
func (s *Server) initUpstreams() {
	s.upstreams = make([]*upstream, 0, len(s.cfg.Machines))
	s.upstreamByName = make(map[string]*upstream, len(s.cfg.Machines))
	var presence homePresence
	if s.cfg.WakeHomePresence.URL != "" {
		presence = newHarborPresenceClient(s.cfg.WakeHomePresence.URL, s.cfg.WakeHomePresence.Token)
	}
	for _, mc := range s.cfg.Machines {
		u := &upstream{name: mc.Name, target: mc.URL}
		u.netmon = newNetMonitor(mc.URL, s.cfg.NetmonInterval, s.cfg.probeIdentity())
		u.proxy = newTTYDProxy(mc.URL, u.netmon)
		if mc.WakeCommand != "" {
			var homeCheck homePresence
			if presence != nil && s.cfg.suppressWakeWhenHome(mc.Name) {
				homeCheck = presence
			}
			u.waker = newUpstreamWakerLocked(mc.WakeCommand, mc.URL.Host, mc.Name, s.store, homeCheck)
		}
		if mc.SessionLeaseCommand != "" {
			u.leaser = newSessionLeaser(mc.SessionLeaseCommand)
		}
		s.upstreams = append(s.upstreams, u)
		s.upstreamByName[mc.Name] = u
	}
}

// primaryUpstream 是默认机器：门禁自身指标与浏览器上报（与机器无关）挂在这一份下。
func (s *Server) primaryUpstream() *upstream {
	if len(s.upstreams) == 0 {
		return nil
	}
	return s.upstreams[0]
}

// New 构造服务。
func New(cfg Config) (*Server, error) {
	if cfg.AuthMode == "" {
		cfg.AuthMode = "local"
		if cfg.SupabaseURL != "" {
			cfg.AuthMode = "supabase"
		}
	}
	if len(cfg.Machines) == 0 || len(cfg.AllowedEmails) == 0 || cfg.SessionTTL < time.Second || cfg.DataDir == "" {
		return nil, fmt.Errorf("incomplete server configuration")
	}
	var verifier TokenVerifier
	var passkeys *localPasskeys
	var passwords *passwordGuard
	var key []byte
	var err error
	switch cfg.AuthMode {
	case "supabase":
		verifier, err = authkit.New(cfg.Auth)
		if err != nil {
			return nil, err
		}
		derived := sha256.Sum256(append([]byte("shellgate-session:v1:"), cfg.Auth.Secret...))
		key = derived[:]
	case "local":
		origin, err := parsePublicURL(cfg.PublicURL)
		if err != nil {
			return nil, err
		}
		if origin.Scheme == "https" && !cfg.CookieSecure {
			return nil, fmt.Errorf("HTTPS requires secure cookies")
		}
		passkeys, err = newLocalPasskeys(cfg.PublicURL)
		if err != nil {
			return nil, err
		}
		passwords, err = newPasswordGuard()
		if err != nil {
			return nil, err
		}
		key, err = loadSessionKey(cfg.DataDir)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported authentication adapter")
	}
	sessionVerifier, err := authkit.New(authkit.Config{Secret: key, Issuers: []string{"shellgate-session"}, Audience: "shellgate"})
	if err != nil {
		return nil, err
	}
	st, err := store.Open(cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if cfg.LegacySQLitePath != "" {
		imported, migErr := st.ImportFromSQLite(cfg.LegacySQLitePath)
		if migErr != nil {
			_ = st.Close()
			return nil, fmt.Errorf("导入旧 SQLite 失败: %w", migErr)
		}
		if imported {
			log.Printf("已从 %s 导入 Tab/预置到共享库", cfg.LegacySQLitePath)
		}
	}
	st.Defaults = cfg.DefaultPresets
	tabs, err := newTabStore(st)
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	s := &Server{
		cfg:             cfg,
		verifier:        verifier,
		sessionSecret:   key,
		sessionIssuer:   "shellgate-session",
		sessionAudience: "shellgate",
		sessionVerifier: sessionVerifier,
		passkeys:        passkeys,
		passwords:       passwords,
		store:           st,
		tabs:            tabs,
		ready:           true,
	}
	s.initUpstreams()
	if err := s.reacquireSessionLeases(); err != nil {
		_ = st.Close()
		return nil, err
	}
	for _, u := range s.upstreams {
		u.netmon.startLoop()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /login", s.handleLogin)
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("GET /api/edge-probe", s.handleEdgeProbe)
	mux.HandleFunc("OPTIONS /api/edge-probe", s.handleEdgeProbe)
	mux.HandleFunc("GET /api/machines", s.handleMachines)
	mux.HandleFunc("POST /api/machines/{name}/ready", s.handleMachineReady)
	mux.HandleFunc("POST /api/session", s.handleSession)
	if s.passkeys != nil {
		mux.HandleFunc("POST /api/password/register", s.handlePasswordRegister)
		mux.HandleFunc("POST /api/password/login", s.handlePasswordLogin)
		mux.HandleFunc("POST /api/passkeys/register/begin", s.handlePasskeyRegisterBegin)
		mux.HandleFunc("POST /api/passkeys/register/finish", s.handlePasskeyRegisterFinish)
		mux.HandleFunc("POST /api/passkeys/login/begin", s.handlePasskeyLoginBegin)
		mux.HandleFunc("POST /api/passkeys/login/finish", s.handlePasskeyLoginFinish)
	}
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /logout", s.handleLogout)
	mux.HandleFunc("GET /api/presets", s.handleListPresets)
	mux.HandleFunc("POST /api/presets", s.handleCreatePreset)
	mux.HandleFunc("PUT /api/presets/{id}", s.handleUpdatePreset)
	mux.HandleFunc("DELETE /api/presets/{id}", s.handleDeletePreset)
	mux.HandleFunc("POST /api/tabs", s.handleCreateTab)
	mux.HandleFunc("GET /api/tabs", s.handleListTabs)
	mux.HandleFunc("DELETE /api/tabs/{id}", s.handleDeleteTab)
	mux.HandleFunc("GET /api/netmon", s.handleNetmon)
	mux.HandleFunc("GET /api/netmon/ping", s.handleNetmonPing)
	mux.HandleFunc("POST /api/netmon/sample", s.handleNetmonSample)
	mux.HandleFunc("GET /__shellgate/enhance.js", s.handleEnhanceJS)
	mux.HandleFunc("GET /__shellgate/external-auth.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write(externalAuthJS)
	})
	mux.HandleFunc("POST /__shellgate/debug-log", s.handleDebugLog)
	mux.HandleFunc("/", s.handleRoot)

	s.httpSrv = &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.protectRequests(s.stripClientAuthHeader(mux)),
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s, nil
}

// reacquireSessionLeases 在门禁重启后为库里仍在的 Mac Tab 重新申请睡眠租约。
// 失败只记日志：Tab 目录仍保留，用户下次主动用 Mac 时再走唤醒/租约路径。
func (s *Server) reacquireSessionLeases() error {
	tabs, err := s.tabs.All()
	if err != nil {
		return err
	}
	for _, tab := range tabs {
		u := s.upstreamByName[tab.Machine]
		if u == nil || u.leaser == nil {
			continue
		}
		if err := u.leaser.acquire(context.Background(), tab.ID); err != nil {
			log.Printf("重启后补申请 Mac 睡眠租约失败 machine=%s tab=%s: %v", tab.Machine, tab.ID, err)
		}
	}
	return nil
}

// ListenAndServe 启动 HTTP 服务。
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	return s.httpSrv.Serve(ln)
}

// Shutdown 优雅关闭。
func (s *Server) Shutdown() error {
	s.mu.Lock()
	s.ready = false
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, u := range s.upstreams {
		u.netmon.stopLoop()
		u.netmon.closeConnections()
	}
	err := s.httpSrv.Shutdown(ctx)
	s.wg.Wait()
	return errors.Join(err, s.store.Close())
}

func (s *Server) stripClientAuthHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del(authHeaderName)
		r.Header.Del("X-WEBAUTH-USER")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	ready := s.ready
	s.mu.RUnlock()
	if !ready {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "starting"})
		return
	}
	if s.store != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.store.Ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "starting"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "running"})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	// 与工作台同口径：禁止嵌进终端 iframe，否则已登录时 302 到 / 会再画一套顶栏。
	denyFraming(w)
	if _, ok := s.identityFromCookie(r); ok {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	// Passkey 只在 apex origin；边缘未登录时跳回登录入口。
	if s.cfg.EdgeRoutingEnabled() && s.cfg.IsEdgeHost(r.Host) {
		http.Redirect(w, r, s.cfg.ApexOrigin()+"/login", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(loginHTML)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"authMode":    s.cfg.AuthMode,
		"supabaseUrl": s.cfg.SupabaseURL,
		"anonKey":     s.cfg.SupabaseAnon,
	}
	if s.cfg.EdgeRoutingEnabled() {
		out["apexOrigin"] = s.cfg.ApexOrigin()
		edges := make([]map[string]string, 0, len(s.cfg.Edges))
		for _, e := range s.cfg.Edges {
			edges = append(edges, map[string]string{
				"name":   e.Name,
				"host":   e.Host,
				"origin": "https://" + e.Host,
			})
		}
		out["edges"] = edges
	}
	writeJSON(w, http.StatusOK, out)
}

// handleEdgeProbe 供浏览器跨边缘测 RTT；不鉴权、无 Cookie。CORS 仅放行 apex 与已配置边缘。
func (s *Server) handleEdgeProbe(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.EdgeRoutingEnabled() {
		http.NotFound(w, r)
		return
	}
	origin := r.Header.Get("Origin")
	if origin != "" {
		if !s.cfg.edgeProbeAllowedOrigin(origin) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Max-Age", "600")
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// machineView 是 /api/machines 的单个条目。机器目录由服务端下发（含展示名），
// 客户端不内置任何机器名--新增机器后所有客户端不刷新即可见。
type machineView struct {
	Name       string `json:"name"`
	Target     string `json:"target"`
	Default    bool   `json:"default"`
	Healthy    bool   `json:"healthy"`
	FailStreak int    `json:"failStreak"`
	Wakeable   bool   `json:"wakeable"`
}

// handleMachines 返回全部上游机器列表（登录态保护）。
func (s *Server) handleMachines(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireIdentity(w, r); !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	defaultName := s.cfg.DefaultMachineName()
	out := make([]machineView, 0, len(s.upstreams))
	for _, u := range s.upstreams {
		out = append(out, machineView{
			Name:       u.name,
			Target:     u.target.Host,
			Default:    u.name == defaultName,
			Healthy:    u.netmon.healthy(),
			FailStreak: u.netmon.failStreak(),
			Wakeable:   u.waker != nil,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"machines": out})
}

func (s *Server) handleMachineReady(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireIdentity(w, r); !ok {
		return
	}
	name := r.PathValue("name")
	u, ok := s.upstreamByName[name]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "机器不存在：" + name})
		return
	}
	woke, err := u.ensureReady(r.Context())
	if err != nil {
		log.Printf("自动唤醒上游失败 machine=%s: %v", name, err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"message": wakeFailureMessage(name, err),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ready": true, "woke": woke})
}

func (u *upstream) ensureReady(ctx context.Context) (bool, error) {
	if u.waker == nil {
		return false, nil
	}
	return u.waker.ensureReady(ctx)
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/login" || strings.HasPrefix(path, "/api/") || path == "/healthz" || path == "/logout" {
		http.NotFound(w, r)
		return
	}
	id, ok := s.identityFromCookie(r)
	if !ok {
		s.redirectToLogin(w, r)
		return
	}
	if strings.HasPrefix(path, "/t/") {
		// apex 不承载终端反代：强制回选路页，避免长连接粘在登录入口主机上。
		if s.cfg.EdgeRoutingEnabled() && s.cfg.IsApexHost(r.Host) {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		s.handleTermProxy(w, r, id.Email)
		return
	}
	if path != "/" {
		http.NotFound(w, r)
		return
	}
	// 终端 iframe 的 src 若误成空 path（拼出 "?rendererType=…"），浏览器会解析成
	// 当前页的 /?…，于是工作台被嵌进 iframe，顶栏出现「双层 title bar」。
	// 顶层打开仍正常；只拦 iframe/embed。/t/{id}/ 反代 ttyd 不受影响。
	if isEmbeddedDocumentRequest(r) {
		http.Error(w, "工作台不能嵌套在终端里", http.StatusForbidden)
		return
	}
	denyFraming(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if s.cfg.EdgeRoutingEnabled() && s.cfg.IsApexHost(r.Host) {
		_, _ = w.Write(edgePickHTML)
		return
	}
	_, _ = w.Write(workspaceHTML)
}

func (s *Server) redirectToLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.EdgeRoutingEnabled() && s.cfg.IsEdgeHost(r.Host) {
		http.Redirect(w, r, s.cfg.ApexOrigin()+"/login", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/login", http.StatusFound)
}

// denyFraming 禁止把登录页/工作台嵌进 iframe（双保险，配合 isEmbeddedDocumentRequest）。
func denyFraming(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
}

// isEmbeddedDocumentRequest 识别浏览器把本响应当 iframe/embed 文档加载的请求。
func isEmbeddedDocumentRequest(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Dest"))) {
	case "iframe", "embed", "object":
		return true
	default:
		return false
	}
}

// handleEnhanceJS 提供终端增强脚本静态资源，同源加载不受 iframe 跨域限制。
func (s *Server) handleEnhanceJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(enhanceJS)
}

func readJSON(r *http.Request, v any) (err error) {
	defer func() {
		if closeErr := r.Body.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("关闭请求正文: %w", closeErr))
		}
	}()
	dec := json.NewDecoder(io.LimitReader(r.Body, (1<<20)+1))
	if err := dec.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected exactly one JSON value")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
