package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/updater"
)

func TestCrashDiscoveryRecognizesOnlyHistoricalUnsupportedOperation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure *protocol.Error
		absent  bool
	}{
		{"historical", protocol.Errorf(protocol.CodeInvalidRequest, `operation "runner.window_target" is not part of the runner protocol`), true},
		{"identity", protocol.Errorf(protocol.CodeInvalidRequest, "supervised runner identity required"), false},
		{"credential", protocol.Errorf(protocol.CodeForbidden, "host credential rejected"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Tend-Host") != "fixture" || r.Header.Get("Authorization") != "Bearer fixture-only" {
					t.Error("missing host credential")
				}
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": tc.failure})
			}))
			defer server.Close()
			client := windowClient{host: "fixture", token: "fixture-only", client: server.Client(), ids: protocol.NewULIDGen(time.Now, nil), endpoint: server.URL}
			target, err := client.Target(t.Context(), updater.Identity{PID: 12, StartTicks: 2, LaunchID: "fixture"})
			if target != "" || (err == nil) != tc.absent {
				t.Fatal(target, err)
			}
		})
	}
}
