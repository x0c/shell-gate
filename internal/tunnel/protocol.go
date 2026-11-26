// Package tunnel provides an outbound HTTPS/WSS multiplexed reverse tunnel
// between NAT-hosted upstream agents and ShellGate gateway instances.
package tunnel

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/hashicorp/yamux"
)

const (
	// TunnelPath is the gateway HTTP path upgraded to a reverse tunnel WebSocket.
	TunnelPath = "/api/agent/tunnel"

	// ServiceTTYD identifies the local terminal upstream service on an agent.
	ServiceTTYD = "ttyd"
	// ServicePresence identifies the local home-presence HTTP service on an agent.
	ServicePresence = "presence"

	typeDial  = "dial"
	typeWake  = "wake"
	typeLease = "lease"

	wsReadLimit         = 4 << 20
	streamHeaderLimit   = 4096
	streamHeaderTimeout = 10 * time.Second
)

// StreamRequest is the single-line JSON header sent on each opened yamux stream.
type StreamRequest struct {
	Type    string `json:"type"`
	Service string `json:"service,omitempty"`
	Machine string `json:"machine,omitempty"`
	Action  string `json:"action,omitempty"`
	TabID   string `json:"tabId,omitempty"`
}

// StreamResponse is the single-line JSON acknowledgement returned by the agent.
type StreamResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// ValidName checks an agent or machine identifier (1..16 ASCII letters, digits, _ or -).
func ValidName(name string) bool {
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

// ValidLeaseAction reports whether action is an allowed session-lease operation.
func ValidLeaseAction(action string) bool {
	return action == "acquire" || action == "renew" || action == "release"
}

// ValidTabID checks a 32-character lowercase hexadecimal tab identifier.
func ValidTabID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 10 * time.Second
	cfg.ConnectionWriteTimeout = 10 * time.Second
	cfg.MaxStreamWindowSize = 512 * 1024
	cfg.LogOutput = io.Discard
	return cfg
}

// readStreamLine reads a single newline-terminated JSON line without over-reading
// into the stream payload that immediately follows the header.
func readStreamLine(r io.Reader, dst any) error {
	var buf [streamHeaderLimit]byte
	var one [1]byte
	n := 0
	for {
		readN, err := r.Read(one[:])
		if readN == 1 {
			if one[0] == '\n' {
				break
			}
			if n >= len(buf) {
				return fmt.Errorf("stream header exceeds %d bytes", streamHeaderLimit)
			}
			buf[n] = one[0]
			n++
		}
		if err != nil {
			if err == io.EOF && n > 0 {
				break
			}
			return err
		}
	}
	if err := json.Unmarshal(buf[:n], dst); err != nil {
		return fmt.Errorf("decode stream header: %w", err)
	}
	return nil
}

func writeStreamLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}
