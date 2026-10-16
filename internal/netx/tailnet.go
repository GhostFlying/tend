package netx

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/tsnet"
)

// TailnetConfig joins the tailnet as an embedded node (D23).
type TailnetConfig struct {
	// Hostname is the node's name on the tailnet.
	Hostname string
	// Dir keeps the node's state; it must persist, or every start
	// registers a new node.
	Dir string
	// AuthKey is needed only the first time, before the state exists.
	AuthKey string
	// Ephemeral nodes are removed when they go offline.
	Ephemeral bool
	// Logf receives tsnet's user-facing log lines; nil discards them.
	Logf func(format string, args ...any)
}

// Tailnet is an embedded tailnet node. It implements WhoIs.
type Tailnet struct {
	srv *tsnet.Server
	lc  *local.Client
	dns string
}

var _ WhoIs = (*Tailnet)(nil)

// JoinTailnet brings the node up.
func JoinTailnet(ctx context.Context, cfg TailnetConfig) (*Tailnet, error) {
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	srv := &tsnet.Server{Hostname: cfg.Hostname, Dir: cfg.Dir, AuthKey: cfg.AuthKey, Ephemeral: cfg.Ephemeral, UserLogf: logf, Logf: func(string, ...any) {}}
	upCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	status, err := srv.Up(upCtx)
	if err != nil {
		srv.Close()
		return nil, fmt.Errorf("netx: join tailnet: %w", err)
	}
	lc, err := srv.LocalClient()
	if err != nil {
		srv.Close()
		return nil, fmt.Errorf("netx: local client: %w", err)
	}
	return &Tailnet{srv: srv, lc: lc, dns: strings.TrimSuffix(status.Self.DNSName, ".")}, nil
}

// DNSName is the node's full name on the tailnet.
func (t *Tailnet) DNSName() string { return t.dns }

// Listen listens on the node's tailnet addresses only.
func (t *Tailnet) Listen(addr string) (net.Listener, error) { return t.srv.Listen("tcp", addr) }

// ListenTLS listens on the node's tailnet addresses with the tailnet's
// certificate for the node's name, issued on the first handshake.
func (t *Tailnet) ListenTLS(addr string) (net.Listener, error) { return t.srv.ListenTLS("tcp", addr) }

// FetchCert obtains the node's certificate ahead of the first client. It
// fails when the tailnet has HTTPS certificates turned off.
func (t *Tailnet) FetchCert(ctx context.Context) error {
	_, _, err := t.lc.CertPair(ctx, t.dns)
	return err
}

// HTTPClient returns a client that dials through the tailnet.
func (t *Tailnet) HTTPClient() *http.Client { return t.srv.HTTPClient() }

// WhoIs implements WhoIs.
func (t *Tailnet) WhoIs(ctx context.Context, remoteAddr string) (Identity, error) {
	w, err := t.lc.WhoIs(ctx, remoteAddr)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %w", ErrUnknownPeer, err)
	}
	id := Identity{Tags: w.Node.Tags}
	if w.Node.ComputedName != "" {
		id.Node = w.Node.ComputedName
	} else {
		id.Node, _, _ = strings.Cut(w.Node.Name, ".")
	}
	if w.UserProfile != nil && len(w.Node.Tags) == 0 {
		id.User = w.UserProfile.LoginName
	}
	return id, nil
}

// Close leaves the tailnet.
func (t *Tailnet) Close() error { return t.srv.Close() }
