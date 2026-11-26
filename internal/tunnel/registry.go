package tunnel

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

var (
	// ErrAgentOffline indicates that the named agent has no active tunnel session.
	ErrAgentOffline = errors.New("agent offline")
	// ErrAgentUnknown indicates that the named agent has no configured token.
	ErrAgentUnknown = errors.New("agent unknown")
)

type agentEntry struct {
	gen         uint64
	session     *yamux.Session
	cancel      context.CancelFunc
	remote      string
	connectedAt time.Time
}

// Registry authenticates inbound agent WebSockets and multiplexes outbound
// service dials and control hooks over active sessions.
type Registry struct {
	tokens   map[string][32]byte
	mu       sync.RWMutex
	sessions map[string]*agentEntry
	nextGen  uint64
	closed   bool
}

// NewRegistry builds a tunnel registry for the configured agent->token map.
func NewRegistry(tokens map[string]string) *Registry {
	hashed := make(map[string][32]byte, len(tokens))
	for name, tok := range tokens {
		hashed[name] = sha256.Sum256([]byte(tok))
	}
	return &Registry{
		tokens:   hashed,
		sessions: make(map[string]*agentEntry),
	}
}

// HasToken reports whether an agent name is configured on this gateway.
func (r *Registry) HasToken(name string) bool {
	if r == nil {
		return false
	}
	_, ok := r.tokens[name]
	return ok
}

// HasAgent reports whether an agent currently has an open tunnel session.
func (r *Registry) HasAgent(name string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	entry, ok := r.sessions[name]
	r.mu.RUnlock()
	return ok && entry != nil && !entry.session.IsClosed()
}

// Authenticate verifies a Bearer token against the configured agent digest.
func (r *Registry) Authenticate(name, bearerToken string) bool {
	if r == nil || !ValidName(name) || bearerToken == "" {
		return false
	}
	expected, ok := r.tokens[name]
	if !ok {
		return false
	}
	sum := sha256.Sum256([]byte(bearerToken))
	return subtle.ConstantTimeCompare(expected[:], sum[:]) == 1
}

// HandleUpgrade authenticates and accepts an agent WebSocket tunnel connection.
func (r *Registry) HandleUpgrade(w http.ResponseWriter, req *http.Request) {
	if r == nil || len(r.tokens) == 0 {
		http.NotFound(w, req)
		return
	}
	if req.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSpace(req.URL.Query().Get("name"))
	token := parseBearerToken(req.Header.Get("Authorization"))
	if !r.Authenticate(name, token) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// InsecureSkipVerify is safe here because auth uses Bearer tokens, never cookies.
	// Compression is disabled so inner ttyd permessage-deflate frames are not double-compressed.
	wsConn, err := websocket.Accept(w, req, &websocket.AcceptOptions{
		CompressionMode:    websocket.CompressionDisabled,
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Printf("agent tunnel upgrade failed name=%s remote=%s: %v", name, req.RemoteAddr, err)
		return
	}
	wsConn.SetReadLimit(wsReadLimit)

	connCtx, connCancel := context.WithCancel(context.Background())
	netConn := websocket.NetConn(connCtx, wsConn, websocket.MessageBinary)
	session, err := yamux.Client(netConn, yamuxConfig())
	if err != nil {
		connCancel()
		_ = wsConn.Close(websocket.StatusInternalError, "multiplex init failed")
		log.Printf("agent tunnel mux init failed name=%s: %v", name, err)
		return
	}

	var prev *agentEntry
	var gen uint64
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = session.Close()
		connCancel()
		return
	}
	r.nextGen++
	gen = r.nextGen
	prev = r.sessions[name]
	r.sessions[name] = &agentEntry{
		gen:         gen,
		session:     session,
		cancel:      connCancel,
		remote:      req.RemoteAddr,
		connectedAt: time.Now(),
	}
	r.mu.Unlock()

	if prev != nil {
		_ = prev.session.Close()
		prev.cancel()
	}

	log.Printf("tunnel agent connected name=%s remote=%s", name, req.RemoteAddr)
	select {
	case <-session.CloseChan():
	case <-connCtx.Done():
	}

	r.mu.Lock()
	if cur := r.sessions[name]; cur != nil && cur.gen == gen {
		delete(r.sessions, name)
	}
	r.mu.Unlock()

	_ = session.Close()
	connCancel()
	log.Printf("tunnel agent disconnected name=%s remote=%s", name, req.RemoteAddr)
}

// DialService opens a multiplexed stream to a named service on the agent.
func (r *Registry) DialService(ctx context.Context, agentName, service string) (net.Conn, error) {
	if service != ServiceTTYD && service != ServicePresence {
		return nil, fmt.Errorf("unsupported tunnel service %q", service)
	}
	stream, err := r.openStream(ctx, agentName)
	if err != nil {
		return nil, err
	}
	stopCancel := context.AfterFunc(ctx, func() { _ = stream.Close() })
	defer stopCancel()

	if err := setStreamDeadline(ctx, stream, streamHeaderTimeout); err != nil {
		_ = stream.Close()
		return nil, err
	}
	if err := writeStreamLine(stream, StreamRequest{Type: typeDial, Service: service}); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("agent %s dial %s write: %w", agentName, service, err)
	}
	var resp StreamResponse
	if err := readStreamLine(stream, &resp); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("agent %s dial %s read: %w", agentName, service, err)
	}
	_ = stream.SetDeadline(time.Time{})
	if !resp.OK {
		_ = stream.Close()
		msg := resp.Error
		if msg == "" {
			msg = "rejected"
		}
		return nil, fmt.Errorf("agent %s dial %s: %s", agentName, service, msg)
	}
	return tunnelConn{Stream: stream}, nil
}

// tunnelConn exposes yamux.Stream's half-close semantics as CloseWrite so
// net.Conn wrappers (such as countingConn) propagate TCP half-close cleanly.
type tunnelConn struct {
	*yamux.Stream
}

func (c tunnelConn) CloseWrite() error {
	return c.Close()
}

// InvokeWake asks the connected agent to run its configured wake hook for machine.
func (r *Registry) InvokeWake(ctx context.Context, agentName, machine string) error {
	if !ValidName(machine) {
		return fmt.Errorf("invalid machine name %q", machine)
	}
	return r.invokeControl(ctx, agentName, StreamRequest{
		Type:    typeWake,
		Machine: machine,
	}, 25*time.Second)
}

// InvokeLease asks the connected agent to run its configured lease hook for machine/tabID.
func (r *Registry) InvokeLease(ctx context.Context, agentName, machine, action, tabID string) error {
	if !ValidName(machine) {
		return fmt.Errorf("invalid machine name %q", machine)
	}
	if !ValidLeaseAction(action) {
		return fmt.Errorf("invalid lease action %q", action)
	}
	if !ValidTabID(tabID) {
		return fmt.Errorf("invalid lease tab id")
	}
	return r.invokeControl(ctx, agentName, StreamRequest{
		Type:    typeLease,
		Machine: machine,
		Action:  action,
		TabID:   tabID,
	}, 12*time.Second)
}

func (r *Registry) invokeControl(ctx context.Context, agentName string, req StreamRequest, defaultTimeout time.Duration) error {
	stream, err := r.openStream(ctx, agentName)
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()
	stopCancel := context.AfterFunc(ctx, func() { _ = stream.Close() })
	defer stopCancel()

	if err := setStreamDeadline(ctx, stream, defaultTimeout); err != nil {
		return err
	}
	if err := writeStreamLine(stream, req); err != nil {
		return fmt.Errorf("agent %s %s write: %w", agentName, req.Type, err)
	}
	var resp StreamResponse
	if err := readStreamLine(stream, &resp); err != nil {
		return fmt.Errorf("agent %s %s read: %w", agentName, req.Type, err)
	}
	if !resp.OK {
		msg := resp.Error
		if msg == "" {
			msg = "hook failed"
		}
		return fmt.Errorf("agent %s %s: %s", agentName, req.Type, msg)
	}
	return nil
}

func (r *Registry) openStream(ctx context.Context, agentName string) (*yamux.Stream, error) {
	if r == nil {
		return nil, ErrAgentOffline
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !r.HasToken(agentName) {
		return nil, ErrAgentUnknown
	}
	r.mu.RLock()
	entry := r.sessions[agentName]
	r.mu.RUnlock()
	if entry == nil || entry.session.IsClosed() {
		return nil, ErrAgentOffline
	}
	stream, err := entry.session.OpenStream()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAgentOffline, err)
	}
	return stream, nil
}

// Close closes all active tunnel sessions during gateway shutdown.
func (r *Registry) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closed = true
	entries := make([]*agentEntry, 0, len(r.sessions))
	for name, entry := range r.sessions {
		entries = append(entries, entry)
		delete(r.sessions, name)
	}
	r.mu.Unlock()
	for _, entry := range entries {
		_ = entry.session.Close()
		entry.cancel()
	}
}

func setStreamDeadline(ctx context.Context, stream net.Conn, fallback time.Duration) error {
	deadline := time.Now().Add(fallback)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if time.Until(deadline) <= 0 {
		return context.DeadlineExceeded
	}
	return stream.SetDeadline(deadline)
}

func parseBearerToken(header string) string {
	const prefix = "bearer "
	trimmed := strings.TrimSpace(header)
	if len(trimmed) < len(prefix) || !strings.EqualFold(trimmed[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(trimmed[len(prefix):])
}
