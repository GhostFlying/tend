//go:build linux

package updater

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestControlServeStatusAndContextCleanup(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- h.s.Serve(ctx) }()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(SocketPath(h.cfg)); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	status, err := Call(context.Background(), h.cfg, "status", ControlRequest{})
	if err != nil || status.InstanceID != h.cfg.InstanceID {
		cancel()
		t.Fatalf("status failed: %v", err)
	}
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor event loop leaked")
	}
	if _, err := os.Stat(SocketPath(h.cfg)); !os.IsNotExist(err) {
		t.Fatal("owned control socket remains")
	}
	if _, err := h.p.Inspect(context.Background(), 10); err != nil {
		t.Fatal("updater shutdown stopped service")
	}
}
func TestAuthorityRejectsRedirectAndKeepsCredentialAtFixedEndpoint(t *testing.T) {
	seen := false
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { seen = true }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	a, err := NewHTTPAuthority(Config{AuthorizeURL: server.URL, HealthURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authorize(context.Background(), Binding{}); err == nil {
		t.Fatal("redirect accepted")
	}
	if seen {
		t.Fatal("authority request escaped fixed endpoint")
	}
}
func TestAuthorityBindsJSONAndRequiresSuccessfulHTTP(t *testing.T) {
	binding := Binding{UpgradeID: "u", InstanceID: "host", ProcessInstanceID: "process", ReleaseID: "release"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got Binding
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil || got != binding {
			t.Error("binding mismatch")
		}
		_ = json.NewEncoder(w).Encode(Grant{Binding: got, Allowed: true, RemainingMillis: 1000})
	}))
	defer server.Close()
	a, err := NewHTTPAuthority(Config{AuthorizeURL: server.URL, HealthURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	g, err := a.Authorize(context.Background(), binding)
	if err != nil || g.Binding != binding || !g.Allowed {
		t.Fatalf("grant failed: %+v %v", g, err)
	}
}
