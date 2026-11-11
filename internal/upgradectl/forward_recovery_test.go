//go:build linux

package upgradectl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/updater"
)

// The legacy app/health handler and MainDrained branch here are the production
// conditions retained from the old release. This fixture is a terminal ledger
// with one confirmation, not a fake Healthy authority.
func TestRecoveryHealthWithActualProcessAndLegacyTerminal(t *testing.T) {
	if os.Getenv("TEND_RECOVERY_HEALTH_TEST") != "1" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRecoveryHealthWithActualProcessAndLegacyTerminal$")
		cmd.Env = append(os.Environ(), "TEND_RECOVERY_HEALTH_TEST=1", "TEND_UPDATER_LAUNCH_ID=successor-test-launch")
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated actual-process fixture: %v\n%s", err, raw)
		}
		return
	}
	ctx := t.Context()
	root, err := os.MkdirTemp("", "tend-rh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	ids := protocol.NewULIDGen(time.Now, nil)
	db, err := store.Open(ctx, filepath.Join(root, "ledger.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	original := store.UpgradeRow{StartRequestID: ids.New(protocol.PrefixRequest), View: protocol.UpgradeView{UpgradeID: "old-201", ReleaseID: "old-release", Status: "completed", Phase: "completed", Revision: 8}, ManifestSHA256: strings.Repeat("a", 64), ServerHash: strings.Repeat("b", 64), ServerInstance: "server", ServerProcess: "old-launch", ServerHealthy: 1}
	if err := db.Write(ctx, func(tx *store.Tx) error { return tx.PutUpgrade(ctx, original) }); err != nil {
		t.Fatal(err)
	}
	actual, err := (updater.LinuxProcess{}).Inspect(ctx, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	plan := protocol.ForwardRecoveryPlan{RecoveryID: "forward-201", RequestID: ids.New(protocol.PrefixRequest), LinkRequestID: ids.New(protocol.PrefixRequest), OriginalRequestID: original.StartRequestID, OriginalLedger: original.View, LedgerRevision: 8, Original: protocol.UpgradeBinding{UpgradeID: "old-201", InstanceID: "server", ProcessInstanceID: "old-launch", ReleaseID: "old-release", ManifestSHA256: original.ManifestSHA256, ArtifactSHA256: original.ServerHash}}
	plan.Successor = protocol.UpgradeBinding{UpgradeID: plan.RecoveryID, InstanceID: "server", ProcessInstanceID: actual.LaunchID, ReleaseID: "new-release", ArtifactSHA256: actual.SHA256, ManifestSHA256: strings.Repeat("c", 64)}
	a := app.New(db, ids, time.Now, app.WithForwardRecoveryStartup(plan.RecoveryID))
	socket := filepath.Join(root, "updater.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	state := updater.State{Format: 2, InstanceID: "server", UpdaterEpoch: "installed-updater-epoch", Recovery: &updater.ForwardRecovery{Plan: plan, Phase: "starting_successor"}}
	var stateFormat atomic.Int64
	stateFormat.Store(2)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			t.Error("unexpected control side effect", r.URL.Path)
		}
		responseState := state
		responseState.Format = int(stateFormat.Load())
		_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: responseState})
	}), ReadHeaderTimeout: time.Second}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(ln) }()
	defer func() { _ = server.Close(); <-done }()
	ready := false
	c, err := New(Config{Instance: "server", ProcessInstance: actual.LaunchID, BinarySHA256: actual.SHA256, ControlSocket: filepath.Join(root, "control.sock"), UpdaterSocket: socket, ReleaseDir: filepath.Join(root, "releases"), RecoveryReady: func(context.Context) error {
		if !ready {
			return errors.New("startup still initializing")
		}
		return nil
	}}, a, ids, func(context.Context) (app.ServerObservation, error) {
		return app.ServerObservation{Healthy: true, MainDrained: false}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []int64{2, 3, 4, 5} {
		stateFormat.Store(format)
		if err := c.InitializeRecovery(ctx, plan.RecoveryID); err != nil {
			t.Fatalf("retained updater format %d rejected forward startup: %v", format, err)
		}
	}
	for _, format := range []int64{1, 6} {
		stateFormat.Store(format)
		if err := c.InitializeRecovery(ctx, plan.RecoveryID); err == nil {
			t.Fatalf("unsupported updater format %d admitted forward startup", format)
		}
	}
	stateFormat.Store(4)
	// Real terminal health remains false even if drainer readiness changes.
	health, err := a.ServerUpgradeHealth(ctx, plan.Original)
	if err != nil || health.Healthy || !health.ServerConfirmed {
		t.Fatal("legacy terminal fixture did not reproduce stale count", health, err)
	}
	legacy, err := New(Config{Instance: "server", ProcessInstance: original.ServerProcess, BinarySHA256: original.ServerHash, ControlSocket: filepath.Join(root, "legacy.sock"), UpdaterSocket: socket, ReleaseDir: filepath.Join(root, "legacy-releases")}, a, ids, func(context.Context) (app.ServerObservation, error) {
		return app.ServerObservation{Healthy: true, MainDrained: false}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacyBody, _ := json.Marshal(plan.Original)
	legacyOut := httptest.NewRecorder()
	legacy.Handler().ServeHTTP(legacyOut, httptest.NewRequest(http.MethodPost, "/updater/health", bytes.NewReader(legacyBody)))
	var legacyHealth protocol.UpgradeHealth
	if legacyOut.Code != http.StatusOK || json.Unmarshal(legacyOut.Body.Bytes(), &legacyHealth) != nil || legacyHealth.Healthy || !legacyHealth.ServerConfirmed {
		t.Fatal("production legacy handler did not reproduce the terminal blocker", legacyOut.Body.String())
	}
	post := func(path string, input any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(input)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		out := httptest.NewRecorder()
		c.Handler().ServeHTTP(out, req)
		return out
	}
	// The old controller must reject the successor binding.
	if out := post("/updater/health", plan.Successor); out.Code != http.StatusConflict {
		t.Fatal("legacy endpoint accepted independent successor", out.Body.String())
	}
	out := post("/updater/recovery/health", plan)
	var h protocol.RecoveryHealth
	if out.Code != http.StatusOK {
		t.Fatal(out.Body.String())
	}
	_ = json.Unmarshal(out.Body.Bytes(), &h)
	if h.Ready || h.Sequence != 0 || !h.Registered || !h.Fenced || len(h.Failures) == 0 {
		t.Fatal("unready startup was healthy", h)
	}
	ready = true
	out = post("/updater/recovery/health", plan)
	_ = json.Unmarshal(out.Body.Bytes(), &h)
	first := h.Sequence
	if !h.Ready || first == 0 || h.Process != actual || h.Epoch != actual.LaunchID {
		t.Fatal("actual successor readiness not proved", h)
	}
	out = post("/updater/recovery/health", plan)
	_ = json.Unmarshal(out.Body.Bytes(), &h)
	if h.Sequence <= first {
		t.Fatal("health sequence did not advance")
	}
	wrong := plan
	wrong.Successor.ArtifactSHA256 = strings.Repeat("d", 64)
	if out := post("/updater/recovery/health", wrong); out.Code != http.StatusConflict {
		t.Fatal("wrong binding accepted")
	}
	read, err := a.GetUpgrade(ctx, "old-201")
	if err != nil || !reflect.DeepEqual(read, original) {
		t.Fatal("legacy history rewritten", read, err)
	}
}
