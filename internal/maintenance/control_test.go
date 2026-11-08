package maintenance

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMaintenanceShutdownClosesOwnedLoopsWithoutStartingAgent(t *testing.T) {
	c, n, cfg, _ := fixture(t)
	// Unix sockets have a smaller path limit than testing's descriptive roots.
	socketDir, err := os.MkdirTemp("", "tend-maint-control-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDir)
	c.cfg.ControlSocket = filepath.Join(socketDir, "control.sock")
	token := filepath.Join(cfg.StateDir, "token")
	if err := os.WriteFile(token, []byte("synthetic-owner"), 0600); err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
		case "/api/v1/issues":
			_, _ = fmt.Fprint(w, `{"items":[],"more":false}`)
		case "/api/v1/events":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "event: start\ndata: {\"event_seq\":1}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	defer remote.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, Probe{URL: remote.URL, TokenFile: token, HTTP: remote.Client()}) }()
	for {
		conn, err := net.DialTimeout("unix", c.cfg.ControlSocket, 20*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("controller did not listen")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("controller leaked an owned loop")
	}
	if n.created != 0 || n.started != 0 {
		t.Fatal("healthy program monitoring invoked an agent")
	}
	if _, err := os.Lstat(c.cfg.ControlSocket); !os.IsNotExist(err) {
		t.Fatal("controller left its listener behind", err)
	}
}
