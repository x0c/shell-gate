package server

import (
	"net/http"

	authkit "github.com/x0c/shell-gate/internal/auth"
	"github.com/x0c/shell-gate/internal/server/store"
)

// TokenVerifier is the session and optional external-login verification boundary.
// Implementations must validate signatures, expiry, issuer and audience.
type TokenVerifier interface {
	Verify(string) (*authkit.Identity, error)
}

// TabRepository owns durable tab metadata. Storage failure must never be
// interpreted as an empty workspace or trigger replacement shell creation.
type TabRepository interface {
	InsertTab(store.Tab) error
	GetTab(string) (*store.Tab, error)
	ListTabs(string) ([]store.Tab, error)
	ListAllTabs() ([]store.Tab, error)
	CountTabs(string) (int, error)
	DeleteTab(string, string) (bool, error)
}

// TerminalBackend receives only an authenticated and account-authorized request.
// The ttyd implementation reads the server-owned identity and command context.
type TerminalBackend interface{ http.Handler }

var _ TokenVerifier = (*authkit.Verifier)(nil)
var _ TabRepository = (*store.Store)(nil)
