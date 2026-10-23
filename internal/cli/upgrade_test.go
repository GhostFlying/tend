package cli

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestLocalUpgradeUsesUnixRevisionAndStableRequestIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix local operator control")
	}
	dir, err := os.MkdirTemp("", "tend-upgrade-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "control.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var start protocol.UpgradeStartRequest
	var change protocol.UpgradeChangeRequest
	calls := 0
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if r.Header.Get("Authorization") != "" {
			t.Error("owner bearer leaked into local service")
		}
		switch r.URL.Path {
		case "/upgrades/start":
			_ = json.NewDecoder(r.Body).Decode(&start)
		case "/upgrades/cancel":
			_ = json.NewDecoder(r.Body).Decode(&change)
		case "/upgrades/status":
			if r.Method != http.MethodGet {
				t.Error("status mutated")
			}
		default:
			t.Error("wrong local upgrade route")
		}
		_ = json.NewEncoder(w).Encode(protocol.UpgradeView{UpgradeID: "batch", Revision: 4, Status: "running", Phase: "runners"})
	})}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	te := newTestEnv(t, "http://unused.invalid")
	req := te.IDs.New(protocol.PrefixRequest)
	if code := te.main("upgrade", "--socket", socket, "--release", dir, "--upgrade-id", "batch", "--request-id", req, "--json"); code != exitOK {
		t.Fatalf("start=%d %s", code, te.errOut)
	}
	mu.Lock()
	if start.RequestID != req || start.UpgradeID != "batch" || start.Release != dir {
		t.Fatalf("identity lost: %+v", start)
	}
	mu.Unlock()
	if code := te.main("upgrade", "cancel", "--socket", socket, "--upgrade-id", "batch", "--revision", "4"); code != exitOK {
		t.Fatalf("cancel=%d %s", code, te.errOut)
	}
	mu.Lock()
	if change.UpgradeID != "batch" || change.ExpectedControlRevision != 4 || !protocol.ValidID(protocol.PrefixRequest, change.RequestID) {
		t.Fatalf("revision lost: %+v", change)
	}
	mu.Unlock()
	if code := te.main("upgrade", "status", "--socket", socket); code != exitOK {
		t.Fatalf("status=%d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 3 {
		t.Fatal("unexpected automatic operation replay")
	}
}
func TestLocalUpgradeLostAnswerNeverReplaysBlindly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix local operator control")
	}
	dir, err := os.MkdirTemp("", "tend-upgrade-unknown-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "control.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	calls := make(chan struct{}, 4)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls <- struct{}{}
		c, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = c.Close()
	})}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	te := newTestEnv(t, "http://unused.invalid")
	req := te.IDs.New(protocol.PrefixRequest)
	if code := te.main("upgrade", "--socket", socket, "--release", dir, "--request-id", req); code != exitError {
		t.Fatalf("lost answer=%d", code)
	}
	if !strings.Contains(te.errOut.String(), req) || !strings.Contains(te.errOut.String(), "preserve the request ID") {
		t.Fatal("unknown answer omitted reconciliation identity")
	}
	if len(calls) != 1 {
		t.Fatal("unknown start was replayed")
	}
}
