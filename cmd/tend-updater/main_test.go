package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestServerReleaseEntryGoesThroughCoordinator(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix control")
	}
	dir, err := os.MkdirTemp("", "tend-updater-entry-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "coordinator.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan protocol.UpgradeStartRequest, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/upgrades/start" {
			t.Error("bypassed release coordinator")
		}
		var request protocol.UpgradeStartRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		received <- request
		_ = json.NewEncoder(w).Encode(protocol.UpgradeView{UpgradeID: request.UpgradeID, Status: "running"})
	})}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	var out bytes.Buffer
	request := protocol.UpgradeStartRequest{RequestID: "request", UpgradeID: "batch", Release: dir}
	if err := startCoordinatedUpgrade(context.Background(), socket, request, &out); err != nil {
		t.Fatal(err)
	}
	if got := <-received; !reflect.DeepEqual(got, request) {
		t.Fatal("coordinator request identity changed")
	}
	var v protocol.UpgradeView
	if err := json.Unmarshal(out.Bytes(), &v); err != nil || v.UpgradeID != "batch" {
		t.Fatal("coordinator answer missing")
	}
}

func TestSelfUpdateDispatchResolvesCanonicalJournalPath(t *testing.T) {
	for _, trailing := range []bool{false, true} {
		t.Run(map[bool]string{false: "canonical", true: "trailing_separator"}[trailing], func(t *testing.T) {
			dir := t.TempDir()
			stateDir := dir
			if trailing {
				stateDir += string(os.PathSeparator)
			}
			cfgPath := filepath.Join(dir, "config.json")
			cfgRaw, _ := json.Marshal(map[string]any{"state_dir": stateDir})
			if err := os.WriteFile(cfgPath, cfgRaw, 0o600); err != nil {
				t.Fatal(err)
			}
			journalPath := filepath.Join(stateDir, "self-update.json")
			// A verified journal exercises the real dispatch decoder without
			// invoking a process manager or mutating a supervised process.
			if err := os.WriteFile(journalPath, []byte(`{"format":1,"phase":"verified","plan":{}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := runSelfUpdate(t.Context(), []string{"apply", "--config", cfgPath, "--plan", journalPath}, &out); err != nil {
				t.Fatal(err)
			}
		})
	}
}
