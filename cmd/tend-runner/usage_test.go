package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func TestUsageReconcileUsesRunningTailnetRunnerWithoutJoiningAnotherNode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("private Unix transport")
	}
	dir, err := os.MkdirTemp("", "tend-usage-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "ctl.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		var env protocol.Envelope
		if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
			t.Error(err)
		}
		if req.URL.Path != "/runner/v1/rpc" || env.Operation != protocol.OpRunnerUsageSources || env.Target.ID != "host" {
			t.Error(req.URL, env)
		}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: json.RawMessage(`[]`)}})
	})}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	configPath := filepath.Join(dir, "runner.json")
	raw, _ := json.Marshal(map[string]any{"home": dir, "host_id": "host", "server_url": "https://unreachable.invalid", "tailnet": map[string]any{"hostname": "existing-runner"}, "updater_socket": socket})
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEND_HOST_TOKEN", "configured-token")
	var out, errout bytes.Buffer
	code := runUsageCommand([]string{"reconcile", "--config", configPath, "--native-home", dir, "--provider", "codex"}, &out, &errout)
	if code != 0 || calls != 1 {
		t.Fatal(code, calls, out.String(), errout.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "runner", "journal.db")); !os.IsNotExist(err) {
		t.Fatal("preview created a journal", err)
	}
}

func TestUsageApplyDelegatesJournalOwnershipToExistingRunner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("private Unix transport")
	}
	for _, tailnet := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "tailnet"}[tailnet], func(t *testing.T) {
			dir, err := os.MkdirTemp("", "tend-usage-live-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			if err := os.MkdirAll(filepath.Join(dir, "runner"), 0700); err != nil {
				t.Fatal(err)
			}
			j, err := journal.Open(t.Context(), filepath.Join(dir, "runner", "journal.db"), time.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			socket := filepath.Join(dir, "ctl.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			pendingReads := 0
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch req.URL.Path {
				case "/runner/v1/usage/pending":
					pendingReads++
					_ = json.NewEncoder(w).Encode([]journal.Request{})
				case "/runner/v1/rpc":
					var env protocol.Envelope
					if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
						t.Error(err)
					}
					if env.Operation != protocol.OpRunnerUsageSources {
						t.Error("unexpected live operation", env)
					}
					_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: json.RawMessage(`[]`)}})
				default:
					http.NotFound(w, req)
				}
			})}
			defer server.Close()
			go func() { _ = server.Serve(listener) }()
			configuration := map[string]any{"home": dir, "host_id": "host", "server_url": "https://unreachable.invalid", "updater_socket": socket}
			if tailnet {
				configuration["tailnet"] = map[string]any{"hostname": "existing-runner"}
			}
			configPath := filepath.Join(dir, "runner.json")
			raw, _ := json.Marshal(configuration)
			if err := os.WriteFile(configPath, raw, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TEND_HOST_TOKEN", "configured-token")
			var out, errout bytes.Buffer
			code := runUsageCommand([]string{"reconcile", "--config", configPath, "--native-home", dir, "--provider", "codex", "--apply"}, &out, &errout)
			if code != 0 || pendingReads != 1 {
				t.Fatal("foreground maintenance opened a second journal writer", code, pendingReads, errout.String())
			}
		})
	}
}
