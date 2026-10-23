package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
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
	if got := <-received; got != request {
		t.Fatal("coordinator request identity changed")
	}
	var v protocol.UpgradeView
	if err := json.Unmarshal(out.Bytes(), &v); err != nil || v.UpgradeID != "batch" {
		t.Fatal("coordinator answer missing")
	}
}
