package cli

import (
	"bytes"
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

func TestUpgradeTextSeparatesUpdaterProofFromServiceState(t *testing.T) {
	var out bytes.Buffer
	writeUpdaterObservation(&out, "server", nil)
	if !strings.Contains(out.String(), "awaiting this batch") || strings.Contains(out.String(), "verified") {
		t.Fatal(out.String())
	}
	out.Reset()
	o := &protocol.UpdaterSelfObservation{Process: protocol.RecoveryIdentity{PID: 41, StartTicks: 10, SHA256: strings.Repeat("a", 64)}, SupervisedProcess: protocol.RecoveryIdentity{PID: 42, SHA256: strings.Repeat("b", 64)}, Receipt: &protocol.UpdaterSelfReceipt{RequestID: "original-request", ReleaseSetSHA256: strings.Repeat("c", 64), RetainedProcess: protocol.RecoveryIdentity{PID: 40, StartTicks: 9}}}
	writeUpdaterObservation(&out, "server", o)
	for _, value := range []string{"handoff verified", "PID 41", "service PID 42", "retained service PID 40", "original-request", strings.Repeat("a", 64)} {
		if !strings.Contains(out.String(), value) {
			t.Fatal(value, out.String())
		}
	}
	out.Reset()
	o.Rejection = &protocol.UpdaterSelfRejection{RequestID: "rejected-request"}
	writeUpdaterObservation(&out, "server", o)
	if !strings.Contains(out.String(), "rejected before supervisor stop") || strings.Contains(out.String(), "verified") || strings.Contains(out.String(), "original-request") {
		t.Fatal(out.String())
	}
}

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
		view := protocol.UpgradeView{UpgradeID: "batch", Revision: 4, Status: "running", Phase: "runners"}
		switch r.URL.Path {
		case "/upgrades/start":
			_ = json.NewDecoder(r.Body).Decode(&start)
		case "/upgrades/cancel":
			_ = json.NewDecoder(r.Body).Decode(&change)
		case "/upgrades/status":
			if r.Method != http.MethodGet {
				t.Error("status mutated")
			}
			view.Phase = "server_updater"
			view.Hosts = []protocol.UpgradeHostView{
				{HostID: "excluded", Status: "skipped", Phase: "skipped"},
				{HostID: "updater-only", Status: "skipped", Phase: "verified", Updater: &protocol.UpdaterSelfObservation{Receipt: &protocol.UpdaterSelfReceipt{RequestID: "original-request"}}},
			}
		default:
			t.Error("wrong local upgrade route")
		}
		_ = json.NewEncoder(w).Encode(view)
	})}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	te := newTestEnv(t, "http://unused.invalid")
	req := te.IDs.New(protocol.PrefixRequest)
	if code := te.main("upgrade", "--socket", socket, "--release", dir, "--upgrade-id", "batch", "--delivery-task", "approved-task", "--park-idle-managers", "--request-id", req, "--json"); code != exitOK {
		t.Fatalf("start=%d %s", code, te.errOut)
	}
	mu.Lock()
	if start.RequestID != req || start.UpgradeID != "batch" || start.Release != dir || start.DeliveryTaskID != "approved-task" || !start.ParkIdleManagers {
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
	if strings.Contains(te.out.String(), "excluded updater:") || !strings.Contains(te.out.String(), "updater-only updater: handoff verified") {
		t.Fatal("skipped scope confused with a verified updater-only host", te.out)
	}
	mu.Lock()
	defer mu.Unlock()
	if code := te.main("upgrade", "cancel", "--socket", socket, "--upgrade-id", "batch", "--revision", "4", "--delivery-task", "approved-task"); code != exitUsage {
		t.Fatalf("delivery task accepted on non-start: %d %s", code, te.errOut)
	}
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
