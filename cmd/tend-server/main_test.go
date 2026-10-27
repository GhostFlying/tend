package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/cfgfile"
)

func TestServerRoutesRunnerFilesAndRPC(t *testing.T) {
	runners := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Route", "runner")
		w.WriteHeader(http.StatusNoContent)
	})
	owner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Route", "owner")
		w.WriteHeader(http.StatusNoContent)
	})
	handler := serverHandler(runners, owner)
	for _, tc := range []struct {
		method, path, route string
	}{
		{http.MethodPost, "/runner/v1/rpc", "runner"},
		{http.MethodPost, "/runner/v1/files?request_id=req_test&name=evidence.txt", "runner"},
		{http.MethodGet, "/runner/v1/files/file_test", "runner"},
		{http.MethodGet, "/api/v1/files/file_test", "owner"},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, nil))
			if recorder.Code != http.StatusNoContent || recorder.Header().Get("X-Route") != tc.route {
				t.Fatalf("route = %d %q, want %q", recorder.Code, recorder.Header().Get("X-Route"), tc.route)
			}
		})
	}
}

func TestExampleConfigLoads(t *testing.T) {
	var c config
	if err := cfgfile.Load("../../docs/examples/tend-server.hujson", &c); err != nil {
		t.Fatal(err)
	}
	if c.Tailnet == nil || c.MainAgent != nil || len(c.Hosts) != 1 || c.Queue.WaitPerHour == nil || *c.Queue.WaitPerHour != 0.1 ||
		c.HostLostAfter == nil || time.Duration(*c.HostLostAfter) != 24*time.Hour {
		t.Errorf("example config = %+v", c)
	}
}

func TestBackgroundAutoModeWorkingDirectories(t *testing.T) {
	root := t.TempDir()
	custom := t.TempDir()
	main, err := mainAgentWorkingDirectory(mainAgentConfig{Cwd: custom}, root)
	if err != nil || main != custom {
		t.Fatalf("main cwd=%q err=%v", main, err)
	}
	a, err := mainAgentWorkingDirectory(mainAgentConfig{Cwd: custom, ConversationID: "work-item:a"}, root)
	if err != nil {
		t.Fatal(err)
	}
	again, err := mainAgentWorkingDirectory(mainAgentConfig{ConversationID: "work-item:a"}, root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := mainAgentWorkingDirectory(mainAgentConfig{ConversationID: "work-item:b"}, root)
	if err != nil {
		t.Fatal(err)
	}
	if a != again || a == b || a == main {
		t.Fatalf("background cwd identity not stable and independent: %q %q %q %q", a, again, b, main)
	}
}
