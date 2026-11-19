package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"tailscale.com/client/local"

	"github.com/GhostFlying/tend/internal/testkit"
)

func TestTailscaledSideChannelKeepsTendCredentialsOnTheServerRequest(t *testing.T) {
	socket := testkit.SocketPath(t, "tailscaled.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	daemon := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(served)
		if r.URL.Path != "/localapi/v0/dial" || r.Header.Get("Dial-Host") != "tend.example.invalid" || r.Header.Get("Dial-Port") != "80" || r.Header.Get("Authorization") != "" {
			t.Error("invalid LocalAPI dial request")
		}
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: ts-dial\r\nConnection: Upgrade\r\n\r\n")
		if err := rw.Flush(); err != nil {
			t.Error(err)
			return
		}
		req, err := http.ReadRequest(rw.Reader)
		if err != nil {
			t.Error(err)
			return
		}
		defer req.Body.Close()
		if req.Host != "tend.example.invalid" || req.URL.Path != "/runner/v1/rpc" || req.Header.Get("Authorization") != "Bearer fixture" || req.Header.Get("X-Tend-Host") != "test-host" {
			t.Error("server did not receive the original host and credential")
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n")
	}))
	daemon.Listener.Close()
	daemon.Listener = ln
	daemon.Start()
	defer daemon.Close()
	client, err := (sideChannelConfig{Kind: "tailscaled"}).client("http://tend.example.invalid", &local.Client{Socket: socket, UseSocketOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://tend.example.invalid/runner/v1/rpc", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer fixture")
	req.Header.Set("X-Tend-Host", "test-host")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("response status %d", res.StatusCode)
	}
	<-served
	daemon.Close()
	if res, err := client.Do(req); err == nil {
		res.Body.Close()
		t.Fatal("unavailable host daemon accepted a request")
	}
}
