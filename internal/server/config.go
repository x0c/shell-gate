package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	authkit "github.com/x0c/shell-gate/internal/auth"
	"github.com/x0c/shell-gate/internal/server/store"
)

const (
	envListen                   = "SHELL_LISTEN"
	envAllowedEmails            = "SHELL_ALLOWED_EMAILS"
	envSupabaseURL              = "SHELL_SUPABASE_URL"
	envSupabaseAnon             = "SHELL_SUPABASE_ANON_KEY"
	envTTYDURL                  = "SHELL_TTYD_URL"
	envTTYDTargets              = "SHELL_TTYD_TARGETS"
	envWakeCommands             = "SHELL_WAKE_COMMANDS"
	envWakeHomePresenceURL      = "SHELL_WAKE_HOME_PRESENCE_URL"
	envWakeHomePresenceToken    = "SHELL_WAKE_HOME_PRESENCE_TOKEN"
	envWakeHomePresenceMachines = "SHELL_WAKE_HOME_PRESENCE_MACHINES"
	envSessionLeaseCommands     = "SHELL_SESSION_LEASE_COMMANDS"
	envCookieName               = "SHELL_COOKIE_NAME"
	envCookieSecure             = "SHELL_COOKIE_SECURE"
	envCookieDomain             = "SHELL_COOKIE_DOMAIN"
	envEdgeHosts                = "SHELL_EDGE_HOSTS"
	envSessionTTL               = "SHELL_SESSION_TTL"
	envDataDir                  = "SHELL_DATA_DIR"
	envDatabaseURL              = "SHELL_DATABASE_URL"
	envNetmonInterval           = "SHELL_NETMON_INTERVAL"
	defaultListen               = "127.0.0.1:18765"
	defaultCookieName           = "shell_session"
	defaultSessionTTL           = 12 * time.Hour
	defaultDataDir              = "./data"
	defaultMachine              = "default"
)

// EdgeHost 是一台门禁边缘入口（按客户端时延选路后粘滞的主机名）。
type EdgeHost struct {
	Name string // 短名，如 hk / edge-b
	Host string // 主机名，如 hk.shell.example.com（不含 scheme）
}

// MachineConfig 是一台上游终端主机（ttyd）。列表顺序即展示顺序，第一台是默认机器。
type MachineConfig struct {
	Name                string
	URL                 *url.URL
	WakeCommand         string
	SessionLeaseCommand string
}

// WakeHomePresence 在发网络唤醒前查询「舒适在家」；未配置则功能关闭。
type WakeHomePresence struct {
	URL      string
	Token    string
	Machines map[string]struct{} // 名单内机器在家时禁止唤醒
}

// Config 描述 shell-gate 运行参数。
type Config struct {
	AuthMode       string
	PublicURL      string
	Auth           authkit.Config
	DefaultPresets []store.DefaultPreset
	DebugLogs      bool
	Listen         string
	AllowedEmails  map[string]struct{}
	SupabaseURL    string
	SupabaseAnon   string
	Machines       []MachineConfig
	CookieName     string
	CookieSecure   bool
	// CookieDomain 生产双入口时设为 shell.example.com，使会话 Cookie 覆盖 apex 与 *.shell.example.com。
	// 本地单机留空（host-only）。配置了 Edges 时必填。
	CookieDomain string
	// Edges 非空时启用客户端时延选路：apex（CookieDomain）只登录/选路，工作台跑在边缘主机名上。
	Edges      []EdgeHost
	SessionTTL time.Duration
	DataDir    string
	// DatabaseURL selects local SQLite by default or optional PostgreSQL.
	DatabaseURL string
	// LegacySQLitePath 若存在且目标库为空，启动时一次性导入旧 shell-gate.db。
	LegacySQLitePath string
	// NetmonInterval 是主动探测上游终端主机的间隔；设为 0 表示只做被动统计，不发探测包。
	NetmonInterval time.Duration
	// WakeHomePresence 可选：Harbor presence 闸门，见 docs/design/2026-09-09-wake-suppress-when-home.md。
	WakeHomePresence WakeHomePresence
}

// ConfigFromEnv 从环境变量加载配置。
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		AuthMode:  strings.TrimSpace(os.Getenv("SHELL_AUTH_MODE")),
		PublicURL: strings.TrimRight(strings.TrimSpace(os.Getenv("SHELL_PUBLIC_URL")), "/"),

		Listen:       getEnv(envListen, defaultListen),
		SupabaseURL:  strings.TrimRight(os.Getenv(envSupabaseURL), "/"),
		SupabaseAnon: os.Getenv(envSupabaseAnon),
		CookieName:   getEnv(envCookieName, defaultCookieName),

		CookieDomain: strings.TrimSpace(os.Getenv(envCookieDomain)),
		DataDir:      getEnv(envDataDir, configDataDir()),
	}
	if cfg.AuthMode == "" {
		cfg.AuthMode = "local"
		if cfg.SupabaseURL != "" {
			cfg.AuthMode = "supabase"
		}
	}
	if cfg.AuthMode == "supabase" {
		auth, err := authkit.ConfigFromEnv()
		if err != nil {
			return Config{}, err
		}
		if _, err := authkit.New(auth); err != nil {
			return Config{}, err
		}
		cfg.Auth = auth
	}
	if cfg.AuthMode != "local" && cfg.AuthMode != "supabase" {
		return Config{}, fmt.Errorf("SHELL_AUTH_MODE must be local or supabase")
	}

	if cfg.AuthMode == "supabase" && (cfg.SupabaseURL == "" || cfg.SupabaseAnon == "") {
		return Config{}, fmt.Errorf("external authentication requires %s and %s", envSupabaseURL, envSupabaseAnon)
	}
	if cfg.AuthMode == "supabase" {
		u, err := url.Parse(cfg.SupabaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return Config{}, fmt.Errorf("%s must be an HTTP(S) URL without credentials or query", envSupabaseURL)
		}
	}
	secure, err := parseBool(envCookieSecure, true)
	if err != nil {
		return Config{}, err
	}
	cfg.CookieSecure = secure
	cfg.DebugLogs, err = parseBool("SHELL_DEBUG_LOGS", false)
	if err != nil {
		return Config{}, err
	}
	if cfg.AuthMode == "local" {
		if cfg.PublicURL == "" {
			cfg.PublicURL = "http://localhost:18765"
		}
		u, err := parsePublicURL(cfg.PublicURL)
		if err != nil {
			return Config{}, err
		}
		if u.Scheme == "http" {
			cfg.CookieSecure = false
		}
		if u.Scheme == "https" && !cfg.CookieSecure {
			return Config{}, fmt.Errorf("HTTPS requires secure cookies")
		}
	}
	if raw := strings.TrimSpace(os.Getenv("SHELL_DEFAULT_PRESETS")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.DefaultPresets); err != nil {
			return Config{}, fmt.Errorf("SHELL_DEFAULT_PRESETS must be a JSON array")
		}
		for _, p := range cfg.DefaultPresets {
			if strings.TrimSpace(p.Name) == "" || strings.TrimSpace(p.Command) == "" {
				return Config{}, fmt.Errorf("default presets require name and command")
			}
		}
	}
	// 会话 TTL 是鉴权边界：非法或非正值必须 fail-fast，禁止静默回落到默认掩盖配错。
	sessionTTL, err := parsePositiveDuration(envSessionTTL, defaultSessionTTL)
	if err != nil {
		return Config{}, err
	}
	cfg.SessionTTL = sessionTTL
	if cfg.SessionTTL < time.Second {
		return Config{}, fmt.Errorf("%s must be at least one second", envSessionTTL)
	}
	// 0 表示只做被动统计；非法值 fail-fast；负值与 0 同义但配置意图不清，直接拒绝。
	netmonInterval, err := parseNonNegativeDuration(envNetmonInterval, defaultNetmonInterval)
	if err != nil {
		return Config{}, err
	}
	cfg.NetmonInterval = netmonInterval
	// 多上游：SHELL_TTYD_TARGETS 是逗号分隔的「名称=地址」列表，例如
	// linux=http://203.0.113.2:7681,tokyo=http://203.0.113.9:7681。
	// 未配置时回退旧的单值 SHELL_TTYD_URL（机器名 default），单机部署升级零改动。
	machines, err := parseMachines(os.Getenv(envTTYDTargets), os.Getenv(envTTYDURL))
	if err != nil {
		return Config{}, err
	}
	machines, err = attachWakeCommands(machines, os.Getenv(envWakeCommands))
	if err != nil {
		return Config{}, err
	}
	if err := validateWakeCommands(machines); err != nil {
		return Config{}, err
	}
	machines, err = attachSessionLeaseCommands(machines, os.Getenv(envSessionLeaseCommands))
	if err != nil {
		return Config{}, err
	}
	if err := validateSessionLeaseCommands(machines); err != nil {
		return Config{}, err
	}
	homePresence, err := parseWakeHomePresence(
		machines,
		os.Getenv(envWakeHomePresenceURL),
		os.Getenv(envWakeHomePresenceToken),
		os.Getenv(envWakeHomePresenceMachines),
	)
	if err != nil {
		return Config{}, err
	}
	cfg.WakeHomePresence = homePresence
	cfg.Machines = machines
	edges, err := parseEdgeHosts(os.Getenv(envEdgeHosts))
	if err != nil {
		return Config{}, err
	}
	if len(edges) > 0 {
		if cfg.CookieDomain == "" {
			return Config{}, fmt.Errorf("配置了 %s 时必须同时设置 %s（会话 Cookie 要跨 apex/边缘子域）", envEdgeHosts, envCookieDomain)
		}
		if !validCookieDomain(cfg.CookieDomain) {
			return Config{}, fmt.Errorf("%s 无效: %q", envCookieDomain, cfg.CookieDomain)
		}
	} else if cfg.CookieDomain != "" && !validCookieDomain(cfg.CookieDomain) {
		return Config{}, fmt.Errorf("%s 无效: %q", envCookieDomain, cfg.CookieDomain)
	}
	if len(edges) > 0 && cfg.AuthMode == "local" {
		return Config{}, fmt.Errorf("local passkeys currently require one gateway origin; use external authentication for multi-edge routing")
	}
	cfg.Edges = edges
	cfg.DatabaseURL = strings.TrimSpace(os.Getenv(envDatabaseURL))
	if cfg.DatabaseURL == "" {
		cfg.DatabaseURL = filepath.Join(cfg.DataDir, "workspace.db")
	}
	cfg.LegacySQLitePath = os.Getenv("SHELL_LEGACY_SQLITE_PATH")

	emails := splitCSV(os.Getenv(envAllowedEmails))
	if len(emails) == 0 {
		return Config{}, fmt.Errorf("%s 为空", envAllowedEmails)
	}
	cfg.AllowedEmails = make(map[string]struct{}, len(emails))
	for _, e := range emails {
		address, err := mail.ParseAddress(e)
		if err != nil || address.Address != e || strings.ContainsAny(e, "\r\n") {
			return Config{}, fmt.Errorf("%s contains an invalid email", envAllowedEmails)
		}
		cfg.AllowedEmails[strings.ToLower(e)] = struct{}{}
	}
	if err := (&http.Cookie{Name: cfg.CookieName, Value: "validation"}).Valid(); err != nil {
		return Config{}, fmt.Errorf("invalid cookie name")
	}
	return cfg, nil
}

func parseBool(key string, fallback bool) (bool, error) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if raw == "" {
		return fallback, nil
	}
	switch raw {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("%s must be a boolean", key)
}

func parsePublicURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("SHELL_PUBLIC_URL must be an HTTP(S) origin without credentials or path")
	}
	local := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && (u.Scheme != "http" || !local) {
		return nil, fmt.Errorf("remote passkeys require HTTPS; HTTP is allowed only on localhost")
	}
	return u, nil
}

// EdgeRoutingEnabled 表示已配置边缘主机，apex 只登录/选路，工作台落在边缘主机名上。
func (c Config) EdgeRoutingEnabled() bool {
	return len(c.Edges) > 0
}

// ApexHost 是登录入口主机名（与 CookieDomain 相同）；未启用边缘选路时为空。
func (c Config) ApexHost() string {
	if !c.EdgeRoutingEnabled() {
		return ""
	}
	return c.CookieDomain
}

// ApexOrigin 是登录入口绝对源，如 https://shell.example.com。
func (c Config) ApexOrigin() string {
	host := c.ApexHost()
	if host == "" {
		return ""
	}
	return "https://" + host
}

// IsApexHost 判断请求 Host（可带端口）是否为登录 apex。
func (c Config) IsApexHost(host string) bool {
	apex := c.ApexHost()
	if apex == "" {
		return false
	}
	return stripHostPort(host) == apex
}

// IsEdgeHost 判断请求 Host 是否为已配置的边缘入口。
func (c Config) IsEdgeHost(host string) bool {
	h := stripHostPort(host)
	for _, e := range c.Edges {
		if e.Host == h {
			return true
		}
	}
	return false
}

// edgeProbeAllowedOrigin 返回跨域探测允许的 Origin（apex + 各边缘）。
func (c Config) edgeProbeAllowedOrigin(origin string) bool {
	origin = strings.TrimSpace(origin)
	if origin == "" || !c.EdgeRoutingEnabled() {
		return false
	}
	if origin == c.ApexOrigin() {
		return true
	}
	for _, e := range c.Edges {
		if origin == "https://"+e.Host {
			return true
		}
	}
	return false
}

// parseEdgeHosts 解析 SHELL_EDGE_HOSTS：逗号分隔的「名称=主机名」，如
// hk=hk.shell.example.com,edge-b=edge-b.shell.example.com。空串表示关闭选路。
func parseEdgeHosts(raw string) ([]EdgeHost, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	seenName := make(map[string]struct{})
	seenHost := make(map[string]struct{})
	out := make([]EdgeHost, 0)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, host, ok := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		host = strings.ToLower(strings.TrimSpace(host))
		if !ok || !validMachineName(name) || !validEdgeHostname(host) {
			return nil, fmt.Errorf("%s 条目 %q 无效：应为 名称=主机名（主机名不含 scheme/端口/路径）", envEdgeHosts, part)
		}
		if _, dup := seenName[name]; dup {
			return nil, fmt.Errorf("%s 里名称 %q 重复", envEdgeHosts, name)
		}
		if _, dup := seenHost[host]; dup {
			return nil, fmt.Errorf("%s 里主机名 %q 重复", envEdgeHosts, host)
		}
		seenName[name] = struct{}{}
		seenHost[host] = struct{}{}
		out = append(out, EdgeHost{Name: name, Host: host})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s 为空", envEdgeHosts)
	}
	if len(out) < 2 {
		return nil, fmt.Errorf("%s 至少需要两个边缘（时延选路才有意义）", envEdgeHosts)
	}
	return out, nil
}

func validCookieDomain(domain string) bool {
	return validEdgeHostname(domain)
}

func validEdgeHostname(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	if strings.ContainsAny(host, ":/\\?#@[]") || strings.Contains(host, "..") {
		return false
	}
	if netIPLooksLike(host) {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if len(label) < 1 || len(label) > 63 {
			return false
		}
		for i, r := range label {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			case r >= 'A' && r <= 'Z':
			case r == '-' && i > 0 && i < len(label)-1:
			default:
				return false
			}
		}
	}
	return true
}

func netIPLooksLike(host string) bool {
	// 边缘入口必须是域名，禁止把裸 IP 配进选路表（HTTPS 证书与 Cookie Domain 都对不上）。
	for _, r := range host {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return host != ""
}

func stripHostPort(host string) string {
	h := strings.TrimSpace(host)
	if h == "" {
		return ""
	}
	// IPv6 bracket form：[::1]:443
	if strings.HasPrefix(h, "[") {
		if i := strings.Index(h, "]"); i >= 0 {
			return strings.ToLower(h[1:i])
		}
	}
	if i := strings.LastIndex(h, ":"); i >= 0 {
		// 避免把域名里仅有的冒号误判；有端口时右侧应全是数字。
		port := h[i+1:]
		if port != "" && onlyDigits(port) {
			return strings.ToLower(h[:i])
		}
	}
	return strings.ToLower(h)
}

func onlyDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// attachSessionLeaseCommands 把受限的「创建/释放 Tab 睡眠租约」入口绑定到机器。
// 命令本身固定，Tab ID 作为独立参数传入，禁止拼接 shell 字符串。
func attachSessionLeaseCommands(machines []MachineConfig, raw string) ([]MachineConfig, error) {
	return attachMachineCommands(machines, raw, envSessionLeaseCommands, func(machine *MachineConfig, command string) {
		machine.SessionLeaseCommand = command
	})
}

// attachWakeCommands 把「机器=绝对可执行文件」绑定到对应上游。命令不接收任何
// 浏览器输入，也不经过 shell 解析；部署脚本自行封装固定的受限唤醒动作。
func attachWakeCommands(machines []MachineConfig, raw string) ([]MachineConfig, error) {
	return attachMachineCommands(machines, raw, envWakeCommands, func(machine *MachineConfig, command string) {
		machine.WakeCommand = command
	})
}

func attachMachineCommands(machines []MachineConfig, raw, envName string, set func(*MachineConfig, string)) ([]MachineConfig, error) {
	if strings.TrimSpace(raw) == "" {
		return machines, nil
	}
	out := append([]MachineConfig(nil), machines...)
	indexes := make(map[string]int, len(out))
	for i, machine := range out {
		indexes[machine.Name] = i
	}
	seen := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, command, ok := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		command = strings.TrimSpace(command)
		if !ok || !validMachineName(name) || command == "" {
			return nil, fmt.Errorf("%s 条目 %q 无效：应为 机器名=/绝对/可执行文件", envName, part)
		}
		if _, dup := seen[name]; dup {
			return nil, fmt.Errorf("%s 里机器名 %q 重复", envName, name)
		}
		index, exists := indexes[name]
		if !exists {
			return nil, fmt.Errorf("%s 引用了未配置的机器 %q", envName, name)
		}
		if !filepath.IsAbs(command) {
			return nil, fmt.Errorf("%s 里机器 %q 的入口必须是绝对路径", envName, name)
		}
		seen[name] = struct{}{}
		set(&out[index], command)
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("%s 为空", envName)
	}
	return out, nil
}

func validateSessionLeaseCommands(machines []MachineConfig) error {
	for _, machine := range machines {
		if machine.SessionLeaseCommand == "" {
			continue
		}
		info, err := os.Stat(machine.SessionLeaseCommand)
		if err != nil {
			return fmt.Errorf("%s 里机器 %q 的租约入口不可用: %w", envSessionLeaseCommands, machine.Name, err)
		}
		if !isExecutableHook(machine.SessionLeaseCommand, info) {
			return fmt.Errorf("%s 里机器 %q 的租约入口不可执行", envSessionLeaseCommands, machine.Name)
		}
	}
	return nil
}

// parseWakeHomePresence 解析在家抑制唤醒配置。URL/Token/机器名单须成套；全空关闭功能。
func parseWakeHomePresence(machines []MachineConfig, rawURL, token, rawMachines string) (WakeHomePresence, error) {
	rawURL = strings.TrimSpace(rawURL)
	token = strings.TrimSpace(token)
	rawMachines = strings.TrimSpace(rawMachines)
	if rawURL == "" && token == "" && rawMachines == "" {
		return WakeHomePresence{}, nil
	}
	if rawURL == "" || token == "" || rawMachines == "" {
		return WakeHomePresence{}, fmt.Errorf("%s / %s / %s 必须同时配置，或全部留空关闭在家抑制",
			envWakeHomePresenceURL, envWakeHomePresenceToken, envWakeHomePresenceMachines)
	}
	u, err := url.ParseRequestURI(rawURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return WakeHomePresence{}, fmt.Errorf("%s must be an HTTP(S) URL without embedded credentials", envWakeHomePresenceURL)
	}
	known := make(map[string]struct{}, len(machines))
	for _, m := range machines {
		known[m.Name] = struct{}{}
	}
	out := WakeHomePresence{
		URL:      rawURL,
		Token:    token,
		Machines: make(map[string]struct{}),
	}
	for _, name := range splitCSV(rawMachines) {
		if !validMachineName(name) {
			return WakeHomePresence{}, fmt.Errorf("%s 含非法机器名 %q", envWakeHomePresenceMachines, name)
		}
		if _, ok := known[name]; !ok {
			return WakeHomePresence{}, fmt.Errorf("%s 引用了未配置的机器 %q", envWakeHomePresenceMachines, name)
		}
		out.Machines[name] = struct{}{}
	}
	if len(out.Machines) == 0 {
		return WakeHomePresence{}, fmt.Errorf("%s 为空", envWakeHomePresenceMachines)
	}
	return out, nil
}

// suppressWakeWhenHome 判断该机器是否启用在家抑制闸门。
func (c Config) suppressWakeWhenHome(machine string) bool {
	if c.WakeHomePresence.URL == "" {
		return false
	}
	_, ok := c.WakeHomePresence.Machines[machine]
	return ok
}

// validateWakeCommands 在启动时确认唤醒入口真实存在且可执行，避免用户点 Mac 后才发现部署漏文件。
func validateWakeCommands(machines []MachineConfig) error {
	for _, machine := range machines {
		if machine.WakeCommand == "" {
			continue
		}
		info, err := os.Stat(machine.WakeCommand)
		if err != nil {
			return fmt.Errorf("%s 里机器 %q 的唤醒命令不可用: %w", envWakeCommands, machine.Name, err)
		}
		if !isExecutableHook(machine.WakeCommand, info) {
			return fmt.Errorf("%s 里机器 %q 的唤醒命令不可执行", envWakeCommands, machine.Name)
		}
	}
	return nil
}

// DefaultMachineName 返回默认机器名（列表第一台，也是「＋ shell」未选择时的落点）。
func (c Config) DefaultMachineName() string {
	if len(c.Machines) == 0 || c.Machines[0].Name == "" {
		return defaultMachine
	}
	return c.Machines[0].Name
}

// machineExists 判断机器名是否已配置（Tab / 预置绑定机器时的校验）。
func (c Config) machineExists(name string) bool {
	for _, m := range c.Machines {
		if m.Name == name {
			return true
		}
	}
	return false
}

// parseMachines 解析上游机器列表：targets 优先（「名称=地址」逗号分隔），
// 未配置时回退单值 ttydURL。机器名拼错、重复、格式非法都直接报错--机器是部署级配置，
// fail-fast 比静默丢掉一台机器安全。
func parseMachines(targets, ttydURL string) ([]MachineConfig, error) {
	if targets == "" {
		if ttydURL == "" {
			return nil, fmt.Errorf("%s 或 %s 未配置", envTTYDTargets, envTTYDURL)
		}
		u, err := parseUpstreamURL(ttydURL)
		if err != nil {
			return nil, fmt.Errorf("%s 无效: %w", envTTYDURL, err)
		}
		return []MachineConfig{{Name: defaultMachine, URL: u}}, nil
	}
	seen := make(map[string]struct{})
	out := make([]MachineConfig, 0)
	for _, part := range strings.Split(targets, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, raw, ok := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		raw = strings.TrimSpace(raw)
		if !ok || !validMachineName(name) {
			return nil, fmt.Errorf("%s 里的机器名 %q 无效：应为 1~16 位字母/数字/下划线/连字符", envTTYDTargets, name)
		}
		if _, dup := seen[name]; dup {
			return nil, fmt.Errorf("%s 里机器名 %q 重复", envTTYDTargets, name)
		}
		u, err := parseUpstreamURL(raw)
		if err != nil {
			return nil, fmt.Errorf("%s 里机器 %q 的地址无效: %w", envTTYDTargets, name, err)
		}
		seen[name] = struct{}{}
		out = append(out, MachineConfig{Name: name, URL: u})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s 为空", envTTYDTargets)
	}
	return out, nil
}

// parseUpstreamURL 校验反代上游地址：只允许 http/https，必须有 Host，禁止 URL 内嵌用户名密码。
// 上游是门禁信任边界内的 ttyd，协议与凭证形态写死，避免 file/unix 等 scheme 或 userinfo 被误配进反代。
func parseUpstreamURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid upstream URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("upstream URL requires http or https")
	}
	if u.Host == "" {
		return nil, fmt.Errorf("upstream URL requires a host")
	}
	if u.User != nil {
		return nil, fmt.Errorf("upstream URL must not contain credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("upstream URL must not contain a query or fragment")
	}
	u.Scheme = scheme
	return u, nil
}

func validMachineName(name string) bool {
	if len(name) < 1 || len(name) > 16 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// probeIdentity 返回上游健康探测使用的身份（白名单里字典序最小的邮箱，保证每次启动一致）。
// 上游 ttyd 靠 X-Webauth-User 鉴权，探测不带身份只会拿到 407，量不到真实处理路径的耗时。
func (c Config) probeIdentity() string {
	best := ""
	for e := range c.AllowedEmails {
		if best == "" || e < best {
			best = e
		}
	}
	return best
}

func (c Config) emailAllowed(email string) bool {
	_, ok := c.AllowedEmails[strings.ToLower(strings.TrimSpace(email))]
	return ok
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// parsePositiveDuration 读取必须为正的时长（会话 TTL 等鉴权相关项）。
// 未设置用默认值；已设置但解析失败或 <=0 则报错，禁止静默回落。
func parsePositiveDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s 无效: %q", key, v)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s 必须为正时长，得到 %q", key, v)
	}
	return d, nil
}

// parseNonNegativeDuration 读取允许为 0 的时长（如网络探测间隔：0=只做被动统计）。
// 未设置用默认值；已设置但解析失败或 <0 则报错。
func parseNonNegativeDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s 无效: %q", key, v)
	}
	if d < 0 {
		return 0, fmt.Errorf("%s 不能为负，得到 %q", key, v)
	}
	return d, nil
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func configDataDir() string {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, "shell-gate")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return defaultDataDir
	}
	return filepath.Join(home, ".config", "shell-gate")
}

// Unix permission bits and Windows native executable paths are different contracts.
func isExecutableHook(path string, info os.FileInfo) bool {
	if info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Ext(path), ".exe")
	}
	return info.Mode()&0o111 != 0
}
