package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestCandidateMaintenanceRequiresMatchingServerControl(t *testing.T) {
	base := controlRunner(t)
	cfg := base.cfg
	cfg.InitialMaintenance = true
	cfg.UpgradeID = "batch"
	r := New(cfg)
	if err := r.loadControl(t.Context()); err != nil {
		t.Fatal(err)
	}
	view, err := r.runtimeObservation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode == protocol.RunnerNormal || view.UpgradeID != "batch" {
		t.Fatalf("candidate admitted early: %+v", view)
	}
	wrong := protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerNormal, UpgradeID: "other"}
	if err := r.applyControl(t.Context(), &wrong, r.cfg.Now(), true); err != nil {
		t.Fatal(err)
	}
	if !r.maintenance() {
		t.Fatal("unrelated server control admitted candidate")
	}
	matching := protocol.RunnerControl{Revision: 2, Mode: protocol.RunnerNormal, UpgradeID: "batch"}
	if err := r.applyControl(t.Context(), &matching, r.cfg.Now(), false); err != nil {
		t.Fatal(err)
	}
	if r.maintenance() {
		t.Fatal("matching server admission ignored")
	}
}

func TestUpdaterAuthorizationReusesDurableRequestAndRevision(t *testing.T) {
	r := controlRunner(t)
	r.cfg.IDs = protocol.NewULIDGen(r.cfg.Now, bytes.NewReader(make([]byte, 128)))
	r.cfg.Log = slog.Default()
	r.c.log = r.cfg.Log
	r.cfg.UpdaterInstanceID = "host"
	var requests []protocol.Envelope
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		json.NewDecoder(q.Body).Decode(&env)
		requests = append(requests, env)
		var binding protocol.UpgradeBinding
		json.Unmarshal(env.Payload, &binding)
		result, _ := json.Marshal(protocol.UpgradeGrant{Binding: binding, Allowed: true})
		json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Result: result}})
	}))
	defer server.Close()
	r.cfg.ServerURL = server.URL
	r.cfg.HTTPClient = server.Client()
	r.c.base = server.URL
	r.c.http = server.Client()
	binding := protocol.UpgradeBinding{InstanceID: "host", UpgradeID: "batch", ProcessInstanceID: "candidate"}
	raw, _ := json.Marshal(binding)
	call := func(r *Runner) {
		t.Helper()
		q := httptest.NewRequest(http.MethodPost, "/updater/authorize", bytes.NewReader(raw))
		w := httptest.NewRecorder()
		r.updaterHandler().ServeHTTP(w, q)
		if w.Code != 200 {
			t.Fatalf("authorize %d %s", w.Code, w.Body.String())
		}
	}
	call(r)
	if err := r.applyControl(t.Context(), &protocol.RunnerControl{Revision: 2, Mode: protocol.RunnerMaintenance}, r.cfg.Now(), true); err != nil {
		t.Fatal(err)
	}
	restarted := New(r.cfg)
	if err := restarted.loadControl(t.Context()); err != nil {
		t.Fatal(err)
	}
	call(restarted)
	if len(requests) != 2 || requests[0].RequestID != requests[1].RequestID || *requests[0].ExpectedControlRevision != *requests[1].ExpectedControlRevision {
		t.Fatalf("authorization replaced after restart: %+v", requests)
	}
	pending, err := r.cfg.Journal.Requests(context.Background())
	if err != nil || len(pending) != 0 {
		t.Fatalf("authorization polluted readiness: %+v %v", pending, err)
	}
}

func TestUpdaterHealthCannotTrustLocalCandidateOrWrongBinding(t *testing.T) {
	r := controlRunner(t)
	r.cfg.UpdaterInstanceID = "host"
	r.cfg.UpgradeID = "batch"
	r.cfg.Build.BinarySHA256 = "digest"
	r.loadControl(t.Context())
	binding := protocol.UpgradeBinding{InstanceID: "host", UpgradeID: "batch", ProcessInstanceID: "wrong", ArtifactSHA256: "digest"}
	raw, _ := json.Marshal(binding)
	q := httptest.NewRequest(http.MethodPost, "/updater/health", bytes.NewReader(raw))
	w := httptest.NewRecorder()
	r.updaterHandler().ServeHTTP(w, q)
	if w.Code != http.StatusConflict {
		t.Fatalf("wrong process accepted: %d", w.Code)
	}
}

func TestCandidateCanConfirmExplicitCancellationBeforeFirstHello(t *testing.T) {
	base := controlRunner(t)
	old := protocol.RunnerControl{Revision: 3, Mode: protocol.RunnerMaintenance, UpgradeID: "batch"}
	if err := base.cfg.Journal.SaveControl(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	cfg := base.cfg
	cfg.InitialMaintenance = true
	cfg.UpgradeID = "batch"
	r := New(cfg)
	cancelled := protocol.RunnerControl{Revision: 4, Mode: protocol.RunnerNormal}
	if err := r.applyControl(t.Context(), &cancelled, r.cfg.Now(), true); err != nil {
		t.Fatal(err)
	}
	if r.maintenance() {
		t.Fatal("newer explicit cancellation left candidate permanently gated")
	}
}

func TestDownloadRequiresServerConfirmedAppliedTargetRevision(t *testing.T) {
	r := controlRunner(t)
	r.cfg.UpdaterSocket = "unused"
	r.cfg.UpdaterControlSocket = "unused"
	r.cfg.UpdaterBundleDir = t.TempDir()
	command := protocol.RunnerControl{Revision: 2, Mode: protocol.RunnerMaintenance, UpgradeID: "batch", Upgrade: &protocol.RunnerUpgradeTarget{ManifestPath: "/manifest", ManifestSHA256: "bad", ManifestSize: 1}}
	if err := r.applyControl(t.Context(), &command, r.cfg.Now(), true); err != nil {
		t.Fatal(err)
	}
	r.resourceReconciled = true
	r.confirmedRuntime = protocol.RunnerRuntime{Mode: protocol.RunnerReady, UpgradeID: "batch", AppliedControlRevision: 1}
	r.maybeUpgrade(t.Context())
	r.wg.Wait()
	if r.upgradePhase != "" {
		t.Fatal("download started before server confirmed applied target revision")
	}
}

func TestUpdaterBridgeDeniedAndUncertainRequestsAcrossNewControlAndRestart(t *testing.T) {
	for _, outcome := range []string{"denied", "definitive-failed", "transport-unknown", "business-unknown"} {
		t.Run(outcome, func(t *testing.T) {
			r := controlRunner(t)
			r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 2)
			r.cfg.Log = slog.Default()
			r.c.log = r.cfg.Log
			r.cfg.UpdaterInstanceID = "host"
			if err := r.applyControl(t.Context(), &protocol.RunnerControl{Revision: 4, Mode: protocol.RunnerMaintenance}, r.cfg.Now(), true); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var requests []protocol.Envelope
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				json.NewDecoder(q.Body).Decode(&env)
				mu.Lock()
				requests = append(requests, env)
				first := len(requests) == 1
				mu.Unlock()
				var b protocol.UpgradeBinding
				json.Unmarshal(env.Payload, &b)
				if first && outcome == "transport-unknown" {
					http.Error(w, "receipt unavailable", http.StatusServiceUnavailable)
					return
				}
				if first && outcome == "definitive-failed" {
					json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpFailed, Error: &protocol.Error{Code: protocol.CodeStaleRevision, Message: "stale"}}})
					return
				}
				if first && outcome == "business-unknown" {
					json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpUnknown, Error: &protocol.Error{Code: protocol.CodeUnknownOutcome, Message: "unknown"}}})
					return
				}
				result, _ := json.Marshal(protocol.UpgradeGrant{Binding: b, Allowed: !(first && outcome == "denied")})
				json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: result}})
			}))
			defer server.Close()
			r.cfg.ServerURL = server.URL
			r.cfg.HTTPClient = server.Client()
			r.c.base = server.URL
			r.c.http = server.Client()
			binding := protocol.UpgradeBinding{InstanceID: "host", UpgradeID: "batch", ProcessInstanceID: "candidate"}
			body, _ := json.Marshal(binding)
			call := func(current *Runner) int {
				w := httptest.NewRecorder()
				q := httptest.NewRequest(http.MethodPost, "/updater/authorize", bytes.NewReader(body))
				current.updaterHandler().ServeHTTP(w, q)
				return w.Code
			}
			firstCode := call(r)
			if outcome == "denied" && firstCode != 200 || outcome != "denied" && firstCode != 503 {
				t.Fatalf("first HTTP outcome %d", firstCode)
			}
			if err := r.applyControl(t.Context(), &protocol.RunnerControl{Revision: 7, Mode: protocol.RunnerMaintenance}, r.cfg.Now(), false); err != nil {
				t.Fatal(err)
			}
			restarted := New(r.cfg)
			if err := restarted.loadControl(t.Context()); err != nil {
				t.Fatal(err)
			}
			if code := call(restarted); code != 200 {
				t.Fatalf("retry HTTP %d", code)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(requests) != 2 {
				t.Fatal(requests)
			}
			same := requests[0].RequestID == requests[1].RequestID
			if outcome == "denied" || outcome == "definitive-failed" {
				if same || *requests[1].ExpectedControlRevision != 7 {
					t.Fatal("definitive denial did not rotate for new control", requests)
				}
			} else if !same || *requests[1].ExpectedControlRevision != 4 {
				t.Fatal("unknown request identity changed", requests)
			}
		})
	}
}
