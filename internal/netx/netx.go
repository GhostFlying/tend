// Package netx identifies peers on the tailnet. The server and the runner
// use tsnet in production; tests use a fixed identity table instead.
package netx

import (
	"context"
	"errors"
	"slices"
	"strings"
)

// Identity is what the tailnet says about a peer.
type Identity struct {
	// Node is the peer's short machine name.
	Node string
	// User is the login of the node's owner; tagged nodes have none.
	User string
	Tags []string
}

// WhoIs looks up the tailnet identity behind a connection's remote address.
type WhoIs interface {
	WhoIs(ctx context.Context, remoteAddr string) (Identity, error)
}

// ErrUnknownPeer is returned when the peer is not on the tailnet.
var ErrUnknownPeer = errors.New("netx: unknown peer")

// Matches reports whether the identity is one of the allowed nodes. An
// entry starting with "tag:" matches a node carrying that tag; any other
// entry matches the node name.
func (id Identity) Matches(allowed []string) bool {
	for _, a := range allowed {
		if strings.HasPrefix(a, "tag:") {
			if slices.Contains(id.Tags, a) {
				return true
			}
		} else if a == id.Node {
			return true
		}
	}
	return false
}

// Static answers every lookup with the same identity, or with
// ErrUnknownPeer when Identity is nil. It stands in for tsnet in tests.
type Static struct {
	Identity *Identity
}

// WhoIs implements WhoIs.
func (s Static) WhoIs(context.Context, string) (Identity, error) {
	if s.Identity == nil {
		return Identity{}, ErrUnknownPeer
	}
	return *s.Identity, nil
}
