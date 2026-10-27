package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/tools"
)

var secret = bytes.Repeat([]byte{7}, 32)

const (
	ownerLogin = "owner@example.com"
	clientID   = "phone-0123456789"
)

// peer is the tailnet identity every request comes from; tests change it.
type peer struct {
	mu sync.Mutex
	id netx.Identity
}

func (p *peer) WhoIs(context.Context, string) (netx.Identity, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.id, nil
}

func (p *peer) set(id netx.Identity) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.id = id
}

type env struct {
	t       *testing.T
	clock   *testkit.Clock
	app     *app.App
	store   *store.Store
	ids     protocol.IDGen
	peer    *peer
	tools   *tools.Service
	context *contextbuilder.Builder
	server  *Server
	url     string
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newEnv(t *testing.T) *env {
	t.Helper()
	clock := testkit.NewClock()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "tend.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ids := testkit.NewIDGen(clock, 5)
	a := app.New(s, ids, clock.Now)
	e := &env{t: t, clock: clock, app: a, store: s, ids: ids, peer: &peer{id: netx.Identity{Node: "laptop", User: ownerLogin}}}
	cb := contextbuilder.New(a, s, ids, contextbuilder.Config{})
	e.context = cb
	e.tools = tools.New(a, s, cb, quiet())
	e.server = New(a, s, e.peer, secret, Config{Owners: []string{ownerLogin, "other-owner@example.com"}, Now: clock.Now}, quiet())
	hs := httptest.NewServer(e.server.Handler())
	t.Cleanup(hs.Close)
	e.url = hs.URL
	return e
}

// do sends a request, with the bearer token if one is given, and decodes
// the JSON answer into out, returning the status.
func (e *env) do(method, path, token string, body any, out any) int {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.url+path, r)
	id, err := e.app.DeploymentID(e.t.Context())
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set(deploymentHeader, id)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

// login creates a token of the given scope for clientID.
func (e *env) login(scope protocol.TokenScope) string {
	e.t.Helper()
	return e.loginAs(clientID, scope)
}

// loginAs creates a token of the given scope for a client.
func (e *env) loginAs(client string, scope protocol.TokenScope) string {
	e.t.Helper()
	var v protocol.SessionView
	st := e.do("POST", "/api/v1/session", "", protocol.SessionRequest{ClientID: client, Scope: scope}, &v)
	if st != http.StatusOK || v.ClientID != client || v.Login != ownerLogin || v.Scope != scope || v.Token == "" {
		e.t.Fatalf("create %s token = %d %+v", scope, st, v)
	}
	return v.Token
}

func TestSessionLifecycle(t *testing.T) {
	e := newEnv(t)
	if st := e.do("GET", "/api/v1/session", "", nil, nil); st != http.StatusUnauthorized {
		t.Errorf("without a token = %d, want 401", st)
	}
	for _, scope := range []protocol.TokenScope{protocol.ScopeOwner} {
		tok := e.login(scope)
		var v protocol.SessionView
		if st := e.do("GET", "/api/v1/session", tok, nil, &v); st != http.StatusOK || v.ClientID != clientID || v.Scope != scope || v.Token != "" {
			t.Errorf("read %s token = %d %+v", scope, st, v)
		}
		if want := e.clock.Now().Add(30 * 24 * time.Hour).UTC(); !v.ExpiresAt.Equal(want) {
			t.Errorf("%s token expires at %v, want %v", scope, v.ExpiresAt, want)
		}
	}
	tok := e.login(protocol.ScopeOwner)
	e.clock.Advance(31 * 24 * time.Hour)
	if st := e.do("GET", "/api/v1/session", tok, nil, nil); st != http.StatusUnauthorized {
		t.Errorf("expired token = %d, want 401", st)
	}
	for _, req := range []protocol.SessionRequest{{ClientID: "short", Scope: protocol.ScopeOwner}, {ClientID: clientID}, {ClientID: clientID, Scope: "admin"}} {
		if st := e.do("POST", "/api/v1/session", "", req, nil); st != http.StatusBadRequest {
			t.Errorf("session request %+v = %d, want 400", req, st)
		}
	}
}

func TestFault_TokenWithoutOwnerNode(t *testing.T) {
	e := newEnv(t)
	tok := e.login(protocol.ScopeOwner)
	// The same token, presented from a device of someone who is not an
	// owner, or from a tagged machine.
	for _, id := range []netx.Identity{{Node: "laptop", User: "stranger@example.com"}, {Node: "ci", Tags: []string{"tag:tend-runner"}}} {
		e.peer.set(id)
		if st := e.do("GET", "/api/v1/session", tok, nil, nil); st != http.StatusForbidden {
			t.Errorf("token from %+v = %d, want 403", id, st)
		}
		if st := e.do("POST", "/api/v1/session", "", protocol.SessionRequest{ClientID: clientID, Scope: protocol.ScopeOwner}, nil); st != http.StatusForbidden {
			t.Errorf("new token for %+v = %d, want 403", id, st)
		}
	}
	// Another owner's device does not inherit this owner's token.
	e.peer.set(netx.Identity{Node: "tablet", User: "other-owner@example.com"})
	if st := e.do("GET", "/api/v1/session", tok, nil, nil); st != http.StatusUnauthorized {
		t.Errorf("token of another owner = %d, want 401", st)
	}
}

func TestOwnerNodes(t *testing.T) {
	e := newEnv(t)
	e.server.cfg.OwnerNodes = map[string]string{"devbox": ownerLogin, "tag:shared": "stranger@example.com"}
	// The owner works on a tagged machine named as theirs.
	e.peer.set(netx.Identity{Node: "devbox", Tags: []string{"tag:dev"}})
	tok := e.login(protocol.ScopeOwner)
	var v protocol.SessionView
	if st := e.do("GET", "/api/v1/session", tok, nil, &v); st != http.StatusOK || v.Login != ownerLogin {
		t.Errorf("session from the owner's tagged machine = %d %+v", st, v)
	}
	// The token is the owner's, so it works from their own devices too.
	e.peer.set(netx.Identity{Node: "laptop", User: ownerLogin})
	if st := e.do("GET", "/api/v1/session", tok, nil, nil); st != http.StatusOK {
		t.Errorf("the same token from the owner's laptop = %d", st)
	}
	// Other tagged machines, and nodes mapped to someone who is not an
	// owner, are not owner devices.
	for _, id := range []netx.Identity{{Node: "buildbox", Tags: []string{"tag:dev"}}, {Node: "ci", Tags: []string{"tag:shared"}}} {
		e.peer.set(id)
		if st := e.do("GET", "/api/v1/session", tok, nil, nil); st != http.StatusForbidden {
			t.Errorf("token from %+v = %d, want 403", id, st)
		}
	}
}

func TestTamperedToken(t *testing.T) {
	e := newEnv(t)
	valid := e.server.sign(token{Login: ownerLogin, ClientID: clientID, Scope: protocol.ScopeAgent, Issued: e.clock.Now().UnixMilli()})
	forged := valid[:len(valid)-2] + "AA"
	// An agent token re-signed as an owner token with another key.
	other := New(e.app, e.store, e.peer, bytes.Repeat([]byte{8}, 32), Config{Owners: []string{ownerLogin}, Now: e.clock.Now}, quiet())
	foreign := other.sign(token{Login: ownerLogin, ClientID: clientID, Scope: protocol.ScopeOwner, Issued: e.clock.Now().UnixMilli()})
	for _, tok := range []string{forged, foreign, "not-a-token"} {
		if st := e.do("GET", "/api/v1/session", tok, nil, nil); st != http.StatusUnauthorized {
			t.Errorf("token %q = %d, want 401", tok, st)
		}
	}
}

func TestOwnerScope(t *testing.T) {
	e := newEnv(t)
	request := func(tok string) *http.Request {
		r := httptest.NewRequest("POST", "/api/v1/ops", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		return r
	}
	if _, err := e.server.owner(request(e.retiredAgentToken(clientID))); protocol.CodeOf(err) != protocol.CodeUnauthenticated {
		t.Errorf("owner request with an agent token = %v, want forbidden", err)
	}
	p, err := e.server.owner(request(e.login(protocol.ScopeOwner)))
	if err != nil || p.Kind != protocol.PrincipalOwner || p.Subject != ownerLogin || p.Client != clientID {
		t.Errorf("owner request with an owner token = %+v, %v", p, err)
	}
	// A cookie is not a credential.
	r := httptest.NewRequest("POST", "/api/v1/ops", nil)
	r.Header.Set("Cookie", "tend_session="+e.login(protocol.ScopeOwner))
	if _, err := e.server.owner(r); protocol.CodeOf(err) != protocol.CodeUnauthenticated {
		t.Errorf("owner request with only a cookie = %v, want unauthenticated", err)
	}
}
