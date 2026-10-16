package protocol

import "time"

// Owner API wire types besides the envelope and the read views.

// TokenScope is what an owner API token may be used for (D49).
type TokenScope string

// Token scopes.
const (
	// ScopeOwner is for the owner's own commands, such as the tend CLI.
	ScopeOwner TokenScope = "owner"
	// ScopeAgent is for an agent the owner works in: its hooks and the
	// tools it calls through the MCP bridge.
	ScopeAgent TokenScope = "agent"
)

// Valid reports whether s is a known scope.
func (s TokenScope) Valid() bool { return s == ScopeOwner || s == ScopeAgent }

// SessionRequest asks for a token for a client the caller generated and
// keeps, such as one tend CLI installation.
type SessionRequest struct {
	ClientID string     `json:"client_id"`
	Scope    TokenScope `json:"scope"`
}

// SessionView describes the token a request carries. Token is set only in
// the answer to a SessionRequest; it goes in the Authorization header as a
// bearer token.
type SessionView struct {
	Login     string     `json:"login"`
	ClientID  string     `json:"client_id"`
	Scope     TokenScope `json:"scope"`
	ExpiresAt time.Time  `json:"expires_at"`
	Token     string     `json:"token,omitempty"`
}
