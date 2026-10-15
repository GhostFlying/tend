package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
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
	t      *testing.T
	clock  *testkit.Clock
	app    *app.App
	store  *store.Store
	ids    protocol.IDGen
	peer   *peer
	server *Server
	url    string
	client *http.Client
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
	e := &env{t: t, clock: clock, app: a, store: s, ids: ids, peer: &peer{id: netx.Identity{Node: "phone", User: ownerLogin}}}
	e.server = New(a, s, e.peer, secret, Config{Owners: []string{ownerLogin, "other-owner@example.com"}, Now: clock.Now}, quiet())
	hs := httptest.NewServer(e.server.Handler())
	t.Cleanup(hs.Close)
	jar, _ := cookiejar.New(nil)
	e.url, e.client = hs.URL, &http.Client{Jar: jar}
	return e
}

// do sends a request and decodes the JSON answer into out, returning the
// status.
func (e *env) do(method, path string, body any, header http.Header, out any) int {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.url+path, r)
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func (e *env) login() {
	e.t.Helper()
	var v protocol.SessionView
	if st := e.do("POST", "/api/v1/session", protocol.SessionRequest{ClientID: clientID}, http.Header{HeaderClient: {clientID}}, &v); st != http.StatusOK || v.ClientID != clientID || v.Login != ownerLogin {
		e.t.Fatalf("create session = %d %+v", st, v)
	}
}

func TestSessionLifecycle(t *testing.T) {
	e := newEnv(t)
	if st := e.do("GET", "/api/v1/session", nil, nil, nil); st != http.StatusUnauthorized {
		t.Errorf("before login = %d, want 401", st)
	}
	e.login()
	var v protocol.SessionView
	if st := e.do("GET", "/api/v1/session", nil, nil, &v); st != http.StatusOK || v.ClientID != clientID {
		t.Errorf("after login = %d %+v", st, v)
	}
	e.clock.Advance(31 * 24 * time.Hour)
	if st := e.do("GET", "/api/v1/session", nil, nil, nil); st != http.StatusUnauthorized {
		t.Errorf("expired session = %d, want 401", st)
	}
	if st := e.do("POST", "/api/v1/session", protocol.SessionRequest{ClientID: "short"}, http.Header{HeaderClient: {"short"}}, nil); st != http.StatusBadRequest {
		t.Errorf("bad client id = %d, want 400", st)
	}
}

func TestFault_CookieWithoutOwnerNode(t *testing.T) {
	e := newEnv(t)
	e.login()
	// The same cookie, presented from a device of someone who is not an
	// owner, or from a tagged machine.
	for _, id := range []netx.Identity{{Node: "laptop", User: "stranger@example.com"}, {Node: "ci", Tags: []string{"tag:tend-runner"}}} {
		e.peer.set(id)
		if st := e.do("GET", "/api/v1/session", nil, nil, nil); st != http.StatusForbidden {
			t.Errorf("session from %+v = %d, want 403", id, st)
		}
		if st := e.do("POST", "/api/v1/session", protocol.SessionRequest{ClientID: clientID}, http.Header{HeaderClient: {clientID}}, nil); st != http.StatusForbidden {
			t.Errorf("new session for %+v = %d, want 403", id, st)
		}
	}
	// Another owner's device does not inherit this owner's session.
	e.peer.set(netx.Identity{Node: "tablet", User: "other-owner@example.com"})
	if st := e.do("GET", "/api/v1/session", nil, nil, nil); st != http.StatusUnauthorized {
		t.Errorf("session of another owner = %d, want 401", st)
	}
}

func TestTamperedCookie(t *testing.T) {
	e := newEnv(t)
	forged := e.server.sign(session{Login: ownerLogin, ClientID: clientID, Issued: e.clock.Now().UnixMilli()})
	forged = forged[:len(forged)-2] + "AA"
	st := e.do("GET", "/api/v1/session", nil, http.Header{"Cookie": {cookieName + "=" + forged}}, nil)
	if st != http.StatusUnauthorized {
		t.Errorf("forged cookie = %d, want 401", st)
	}
}

func TestFault_CrossSiteWrite(t *testing.T) {
	e := newEnv(t)
	e.login()
	u := e.url + "/api/v1/session"
	cookies := e.client.Jar.Cookies(mustURL(t, u))
	// A cross-site form can post a session request, but not set the header.
	if st := e.do("POST", "/api/v1/session", protocol.SessionRequest{ClientID: "attacker-client"}, nil, nil); st != http.StatusForbidden {
		t.Errorf("session request without the client header = %d, want 403", st)
	}
	if got := e.client.Jar.Cookies(mustURL(t, u)); len(got) != 1 || got[0].Value != cookies[0].Value {
		t.Error("a session request without the client header replaced the session")
	}
	for _, header := range []string{"", "someone-elses-client"} {
		r := httptest.NewRequest("POST", "/api/v1/ops", nil)
		for _, c := range cookies {
			r.AddCookie(c)
		}
		if header != "" {
			r.Header.Set(HeaderClient, header)
		}
		if _, err := e.server.owner(r, true); protocol.CodeOf(err) != protocol.CodeForbidden {
			t.Errorf("write with client header %q = %v, want forbidden", header, err)
		}
	}
	r := httptest.NewRequest("POST", "/api/v1/ops", nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	r.Header.Set(HeaderClient, clientID)
	p, err := e.server.owner(r, true)
	if err != nil || p.Kind != protocol.PrincipalOwner || p.Subject != ownerLogin || p.Client != clientID {
		t.Errorf("write with the session's client = %+v, %v", p, err)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
