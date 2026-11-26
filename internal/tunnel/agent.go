package tunnel

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

const (
	wakeHookTimeout   = 25 * time.Second
	leaseHookTimeout  = 12 * time.Second
	localDialTimeout  = 5 * time.Second
	wallJumpTick      = 2 * time.Second
	wallJumpThreshold = 8 * time.Second
	minBackoff        = time.Second
	maxBackoff        = 15 * time.Second
	healthySessionAge = 30 * time.Second
)

// AgentConfig defines an outbound reverse-tunnel agent running near upstream services.
type AgentConfig struct {
	Name          string
	Gateways      []string
	Token         string
	TTYDAddr      string
	PresenceAddr  string
	WakeHooks     map[string]string
	LeaseHooks    map[string]string
	AllowEnvProxy bool
}

// Agent maintains persistent WSS/yamux tunnels to one or more gateways and serves
// local dial, wake, and lease streams without holding OS sleep assertions.
type Agent struct {
	cfg      AgentConfig
	wsURLs   []string
	mu       sync.Mutex
	sessions map[int]activeGateway
}

type activeGateway struct {
	session *yamux.Session
	cancel  context.CancelFunc
}

// NewAgent validates cfg and constructs an Agent ready to Run.
func NewAgent(cfg AgentConfig) (*Agent, error) {
	cfg.Name = strings.TrimSpace(cfg.Name)
	if !ValidName(cfg.Name) {
		return nil, fmt.Errorf("agent name %q must be 1..16 characters of [a-zA-Z0-9_-]", cfg.Name)
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("agent token is required")
	}
	if len(cfg.Gateways) == 0 {
		return nil, fmt.Errorf("at least one gateway URL is required")
	}
	wsURLs := make([]string, 0, len(cfg.Gateways))
	seen := make(map[string]struct{}, len(cfg.Gateways))
	for _, raw := range cfg.Gateways {
		wsURL, err := NormalizeGatewayWSURL(raw, cfg.Name)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[wsURL]; dup {
			return nil, fmt.Errorf("duplicate gateway %q", raw)
		}
		seen[wsURL] = struct{}{}
		wsURLs = append(wsURLs, wsURL)
	}
	if cfg.TTYDAddr != "" {
		addr, err := NormalizeLocalAddr(cfg.TTYDAddr)
		if err != nil {
			return nil, fmt.Errorf("ttyd target: %w", err)
		}
		cfg.TTYDAddr = addr
	}
	if cfg.PresenceAddr != "" {
		addr, err := NormalizeLocalAddr(cfg.PresenceAddr)
		if err != nil {
			return nil, fmt.Errorf("presence target: %w", err)
		}
		cfg.PresenceAddr = addr
	}
	if err := validateHookMap("wake", cfg.WakeHooks); err != nil {
		return nil, err
	}
	if err := validateHookMap("lease", cfg.LeaseHooks); err != nil {
		return nil, err
	}
	return &Agent{
		cfg:      cfg,
		wsURLs:   wsURLs,
		sessions: make(map[int]activeGateway),
	}, nil
}

// NormalizeGatewayWSURL validates a gateway base URL and returns its ws(s) tunnel endpoint URL.
func NormalizeGatewayWSURL(raw, agentName string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid gateway URL %q", raw)
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		u.Scheme = "wss"
	case "http", "ws":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("gateway URL %q must use http, https, ws or wss", raw)
	}
	basePath := strings.TrimRight(u.Path, "/")
	if basePath == TunnelPath {
		u.Path = TunnelPath
	} else {
		u.Path = basePath + TunnelPath
	}
	q := url.Values{}
	q.Set("name", agentName)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// NormalizeLocalAddr accepts host:port or http://host:port and normalizes it to host:port.
func NormalizeLocalAddr(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("empty address")
	}
	if strings.Contains(trimmed, "://") {
		u, err := url.Parse(trimmed)
		if err != nil || !strings.EqualFold(u.Scheme, "http") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return "", fmt.Errorf("invalid local address %q", raw)
		}
		trimmed = u.Host
	}
	host, portStr, err := net.SplitHostPort(trimmed)
	if err != nil || host == "" {
		return "", fmt.Errorf("local address %q must be host:port", raw)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid port in %q", raw)
	}
	return net.JoinHostPort(host, portStr), nil
}

func validateHookMap(kind string, hooks map[string]string) error {
	for machine, path := range hooks {
		if !ValidName(machine) {
			return fmt.Errorf("%s hook machine %q is invalid", kind, machine)
		}
		if !filepath.IsAbs(path) {
			return fmt.Errorf("%s hook for %q must be an absolute path", kind, machine)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("%s hook for %q unavailable: %w", kind, machine, err)
		}
		if info.IsDir() || info.Mode()&0o111 == 0 {
			return fmt.Errorf("%s hook for %q is not executable", kind, machine)
		}
	}
	return nil
}

// Run connects to all configured gateways and blocks until ctx is canceled.
func (a *Agent) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.watchWallClockJump(ctx)
	}()
	for idx, wsURL := range a.wsURLs {
		wg.Add(1)
		go func(i int, target string) {
			defer wg.Done()
			a.runGatewayLoop(ctx, i, target)
		}(idx, wsURL)
	}
	wg.Wait()
	a.resetSessions()
	return ctx.Err()
}

// watchWallClockJump detects host sleep/wake jumps via wall-clock progress and resets
// active sockets so reconnect loops re-establish fresh tunnels within ~1-2 seconds.
func (a *Agent) watchWallClockJump(ctx context.Context) {
	ticker := time.NewTicker(wallJumpTick)
	defer ticker.Stop()
	prevWall := time.Now().Round(0)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			wallNow := now.Round(0)
			delta := wallNow.Sub(prevWall)
			prevWall = wallNow
			if delta > wallJumpThreshold || delta < -2*time.Second {
				log.Printf("agent detected clock/sleep jump (%s); resetting gateway sessions", delta.Round(time.Millisecond))
				a.resetSessions()
			}
		}
	}
}

func (a *Agent) resetSessions() {
	a.mu.Lock()
	active := make([]activeGateway, 0, len(a.sessions))
	for idx, entry := range a.sessions {
		active = append(active, entry)
		delete(a.sessions, idx)
	}
	a.mu.Unlock()
	for _, entry := range active {
		_ = entry.session.Close()
		entry.cancel()
	}
}

func (a *Agent) runGatewayLoop(ctx context.Context, idx int, wsURL string) {
	backoff := minBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		started := time.Now()
		err := a.serveGatewayOnce(ctx, idx, wsURL)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) >= healthySessionAge {
			backoff = minBackoff
		}
		log.Printf("agent tunnel disconnected gateway=%s err=%v; reconnecting in %s", redactGatewayURL(wsURL), err, backoff)
		if !sleepWithJitter(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func (a *Agent) serveGatewayOnce(ctx context.Context, idx int, wsURL string) error {
	dialCtx, dialCancel := context.WithTimeout(ctx, 12*time.Second)
	proxyFn := http.ProxyFromEnvironment
	if !a.cfg.AllowEnvProxy {
		proxyFn = nil
	}
	httpClient := &http.Client{
		Transport: &http.Transport{
			Proxy:               proxyFn,
			DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:   false,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}
	wsConn, resp, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		HTTPClient:      httpClient,
		HTTPHeader:      http.Header{"Authorization": {"Bearer " + a.cfg.Token}},
		CompressionMode: websocket.CompressionDisabled,
	})
	dialCancel()
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		if resp != nil {
			return fmt.Errorf("HTTP %d: %w", resp.StatusCode, err)
		}
		return err
	}
	wsConn.SetReadLimit(wsReadLimit)

	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()
	netConn := websocket.NetConn(connCtx, wsConn, websocket.MessageBinary)
	session, err := yamux.Server(netConn, yamuxConfig())
	if err != nil {
		_ = wsConn.Close(websocket.StatusInternalError, "mux init failed")
		return err
	}
	defer func() { _ = session.Close() }()

	a.mu.Lock()
	a.sessions[idx] = activeGateway{session: session, cancel: connCancel}
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if cur, ok := a.sessions[idx]; ok && cur.session == session {
			delete(a.sessions, idx)
		}
		a.mu.Unlock()
	}()

	log.Printf("agent tunnel connected name=%s gateway=%s", a.cfg.Name, redactGatewayURL(wsURL))
	for {
		stream, err := session.AcceptStream()
		if err != nil {
			return err
		}
		go a.handleStream(connCtx, stream)
	}
}

func (a *Agent) handleStream(ctx context.Context, stream *yamux.Stream) {
	closedByPipe := false
	defer func() {
		if !closedByPipe {
			_ = stream.Close()
		}
	}()

	if err := stream.SetReadDeadline(time.Now().Add(streamHeaderTimeout)); err != nil {
		return
	}
	var req StreamRequest
	if err := readStreamLine(stream, &req); err != nil {
		_ = writeStreamLine(stream, StreamResponse{OK: false, Error: err.Error()})
		return
	}
	_ = stream.SetDeadline(time.Time{})

	switch req.Type {
	case typeDial:
		closedByPipe = a.handleDialStream(ctx, stream, req.Service)
	case typeWake:
		a.handleWakeStream(ctx, stream, req.Machine)
	case typeLease:
		a.handleLeaseStream(ctx, stream, req)
	default:
		_ = writeStreamLine(stream, StreamResponse{OK: false, Error: "unsupported stream type"})
	}
}

func (a *Agent) handleDialStream(ctx context.Context, stream *yamux.Stream, service string) bool {
	var targetAddr string
	switch service {
	case ServiceTTYD:
		targetAddr = a.cfg.TTYDAddr
	case ServicePresence:
		targetAddr = a.cfg.PresenceAddr
	default:
		_ = writeStreamLine(stream, StreamResponse{OK: false, Error: "unknown service"})
		return false
	}
	if targetAddr == "" {
		_ = writeStreamLine(stream, StreamResponse{OK: false, Error: fmt.Sprintf("service %q not configured", service)})
		return false
	}
	dialCtx, cancel := context.WithTimeout(ctx, localDialTimeout)
	localConn, err := (&net.Dialer{Timeout: localDialTimeout, KeepAlive: 30 * time.Second}).DialContext(dialCtx, "tcp", targetAddr)
	cancel()
	if err != nil {
		_ = writeStreamLine(stream, StreamResponse{OK: false, Error: err.Error()})
		return false
	}
	if err := writeStreamLine(stream, StreamResponse{OK: true}); err != nil {
		_ = localConn.Close()
		return false
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(localConn, stream)
		if cw, ok := localConn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = localConn.Close()
		}
	}()
	_, _ = io.Copy(stream, localConn)
	_ = stream.Close()
	_ = localConn.Close()
	wg.Wait()
	return true
}

func (a *Agent) handleWakeStream(ctx context.Context, stream *yamux.Stream, machine string) {
	if !ValidName(machine) {
		_ = writeStreamLine(stream, StreamResponse{OK: false, Error: "invalid machine"})
		return
	}
	hook, ok := a.cfg.WakeHooks[machine]
	if !ok {
		_ = writeStreamLine(stream, StreamResponse{OK: false, Error: fmt.Sprintf("no wake hook for %q", machine)})
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, wakeHookTimeout)
	defer cancel()
	if err := exec.CommandContext(runCtx, hook).Run(); err != nil {
		_ = writeStreamLine(stream, StreamResponse{OK: false, Error: err.Error()})
		return
	}
	_ = writeStreamLine(stream, StreamResponse{OK: true})
}

func (a *Agent) handleLeaseStream(ctx context.Context, stream *yamux.Stream, req StreamRequest) {
	if !ValidName(req.Machine) || !ValidLeaseAction(req.Action) || !ValidTabID(req.TabID) {
		_ = writeStreamLine(stream, StreamResponse{OK: false, Error: "invalid lease request"})
		return
	}
	hook, ok := a.cfg.LeaseHooks[req.Machine]
	if !ok {
		_ = writeStreamLine(stream, StreamResponse{OK: false, Error: fmt.Sprintf("no lease hook for %q", req.Machine)})
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, leaseHookTimeout)
	defer cancel()
	if err := exec.CommandContext(runCtx, hook, req.Action, req.TabID).Run(); err != nil {
		_ = writeStreamLine(stream, StreamResponse{OK: false, Error: err.Error()})
		return
	}
	_ = writeStreamLine(stream, StreamResponse{OK: true})
}

func sleepWithJitter(ctx context.Context, base time.Duration) bool {
	var b [8]byte
	_, _ = rand.Read(b[:])
	jitterMax := max(int64(base/5), 1)
	jitter := time.Duration(binary.LittleEndian.Uint64(b[:]) % uint64(jitterMax))
	timer := time.NewTimer(base + jitter)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func redactGatewayURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Scheme + "://" + u.Host
}

// ParseMapping parses comma-separated key=value entries into a map, rejecting empty or duplicate keys.
func ParseMapping(raw string) (map[string]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	out := make(map[string]string)
	for _, part := range strings.Split(trimmed, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("invalid mapping entry %q", part)
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("duplicate key %q", k)
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("mapping cannot be empty")
	}
	return out, nil
}

// Errors returned by DialService / InvokeWake / InvokeLease can wrap ErrAgentOffline.
func IsOfflineError(err error) bool {
	return errors.Is(err, ErrAgentOffline)
}
