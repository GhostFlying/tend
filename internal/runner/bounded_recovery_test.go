package runner

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestBoundedRecoveryStartupHintCannotAuthorizeItself(t *testing.T) {
	t.Setenv("TEND_BOUNDED_RECOVERY_REQUEST_ID", "explicit-recovery-request")
	r := controlRunner(t)
	r.cfg.Log = slog.New(slog.DiscardHandler)
	r.c.log = r.cfg.Log
	r.cfg.RetryDelay = time.Millisecond
	r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 1)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unavailable"})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	err := r.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "bounded recovery") {
		t.Fatalf("unverified startup hint was not refused: %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("unverified force successor contacted business server %d times", requests.Load())
	}
}

func TestBoundedRecoveryUnhintedRestartPreservesWorkAgainstOrdinaryControls(t *testing.T) {
	for _, verdict := range []string{protocol.VerdictContinue, protocol.VerdictStop, protocol.VerdictContinueRetained} {
		t.Run(verdict, func(t *testing.T) {
			r, as, path := retainedStrictFixture(t)
			authority := protocol.BoundedMaintenanceAuthorization{RequestID: "original-replacement-authority", Role: "runner"}
			bindRunnerBoundedTestAuthority(t, r.cfg.Journal, &authority)
			if err := r.cfg.Journal.QuarantineBoundedRecovery(t.Context(), authority); err != nil {
				t.Fatal(err)
			}
			before, requests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
			if err := r.cfg.Journal.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := journal.Open(t.Context(), path, r.cfg.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			cfg := r.cfg
			cfg.Journal = reopened
			r = New(cfg)
			if err := r.loadBoundedRecovery(t.Context()); err != nil {
				t.Fatal(err)
			}
			resume := &protocol.RetainedRunResume{RequestID: "old-attachment-permission", Mode: "attach"}
			retainedStrictHello(t, r, protocol.RunVerdict{RunID: as.RunID, Verdict: verdict, Desired: protocol.DesiredRun, RetainedResume: resume})
			if err := r.hello(t.Context()); err != nil {
				t.Fatal(err)
			}
			r.wg.Wait()
			after, afterRequests := retainedStrictSnapshot(t, reopened, as.RunID)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(requests, afterRequests) || r.lookup(as.RunID) != nil {
				t.Fatalf("ordinary control changed quarantined work: before=%+v after=%+v requests=%+v", before, after, afterRequests)
			}
			runtime, err := r.runtimeObservation(t.Context())
			if err != nil || runtime.Mode != protocol.RunnerMaintenance || runtime.BoundedRecoveryAuthorizationID != authority.RequestID ||
				!slices.Contains(runtime.Blockers, "bounded_recovery_quarantine") || !slices.Contains(runtime.Capabilities, protocol.CapabilityBoundedRunnerRecovery) {
				t.Fatalf("recovery was presented as ready: %+v %v", runtime, err)
			}
		})
	}
}

func TestBoundedRecoveryReplaysOriginalFactsWithoutNativeOrCleanup(t *testing.T) {
	r := controlRunner(t)
	r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 11)
	r.cfg.Log = slog.New(slog.DiscardHandler)
	r.c.log = r.cfg.Log
	as := protocol.Assignment{RunID: "retained-manager", LeaseID: "original-lease", Role: protocol.RoleIssueManager, Protocol: protocol.CurrentRunnerProtocol}
	if err := r.cfg.Journal.AcceptAssignment(t.Context(), as, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.ConfirmStopped(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	original := []protocol.Envelope{
		{RequestID: "original-close", Operation: protocol.OpManagerNativeReply, Target: runTarget(as), Payload: json.RawMessage(`{"id":"native-close","last_sequence":1}`)},
		{RequestID: "original-stop", Operation: protocol.OpManagerNativeStopped, Target: runTarget(as), Payload: json.RawMessage(`{"last_sequence":1}`)},
		{RequestID: "original-event", Operation: protocol.OpManagerNativeEvent, Target: runTarget(as), Payload: json.RawMessage(`{"sequence":1,"body":{}}`)},
		{RequestID: "unknown-native-claim", Operation: protocol.OpManagerNativeClaim, Target: runTarget(as)},
		{RequestID: "unknown-workspace-cleanup", Operation: protocol.OpWorkspaceCleanup, Target: runTarget(as)},
	}
	for _, env := range original {
		if err := r.cfg.Journal.PutRequest(t.Context(), journal.Request{RunID: as.RunID, Envelope: env}); err != nil {
			t.Fatal(err)
		}
	}
	live := protocol.Assignment{RunID: "possibly-live-manager", LeaseID: "live-original-lease", Role: protocol.RoleIssueManager, Protocol: protocol.CurrentRunnerProtocol}
	if err := r.cfg.Journal.AcceptAssignment(t.Context(), live, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.SetHeld(t.Context(), live.RunID, true); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.PutRequest(t.Context(), journal.Request{RunID: live.RunID, Envelope: protocol.Envelope{
		RequestID: "stop-without-native-exit-proof", Operation: protocol.OpManagerNativeStopped, Target: runTarget(live), Payload: json.RawMessage(`{"last_sequence":0}`),
	}}); err != nil {
		t.Fatal(err)
	}
	authority := protocol.BoundedMaintenanceAuthorization{RequestID: "independent-force", Role: "runner"}
	bindRunnerBoundedTestAuthority(t, r.cfg.Journal, &authority)
	if err := r.cfg.Journal.QuarantineBoundedRecovery(t.Context(), authority); err != nil {
		t.Fatal(err)
	}
	if err := r.loadBoundedRecovery(t.Context()); err != nil {
		t.Fatal(err)
	}
	received := make(chan protocol.Envelope, len(original))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		received <- env
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	r.replayBoundedFacts(t.Context())
	var seen []protocol.Envelope
	for len(received) > 0 {
		seen = append(seen, <-received)
	}
	if len(seen) != 3 || seen[0].RequestID != "original-event" || seen[1].RequestID != "original-stop" || seen[2].RequestID != "original-close" {
		t.Fatalf("fact replay changed identities/order or invoked another operation: %+v", seen)
	}
	for _, env := range seen {
		for _, expected := range original {
			if expected.RequestID == env.RequestID && !reflect.DeepEqual(env, expected) {
				t.Errorf("fact envelope changed: got=%+v want=%+v", env, expected)
			}
		}
	}
	pending, err := r.cfg.Journal.Requests(t.Context())
	if err != nil || len(pending) != 3 {
		t.Fatalf("unknown operations were not retained: %+v %v", pending, err)
	}
	run, err := r.cfg.Journal.Run(t.Context(), as.RunID)
	if err != nil || !run.Fenced || !run.StoppedConfirmed || !r.boundedRecoveryPending() || r.lookup(as.RunID) != nil {
		t.Fatalf("transport acknowledgement released quarantined resources: %+v %v", run, err)
	}
	liveRun, err := r.cfg.Journal.Run(t.Context(), live.RunID)
	if err != nil || !liveRun.Held || !liveRun.Fenced || liveRun.StoppedConfirmed {
		t.Fatalf("runner replacement invented native worker exit: %+v %v", liveRun, err)
	}
}

func bindRunnerBoundedTestAuthority(t *testing.T, j *journal.Journal, authority *protocol.BoundedMaintenanceAuthorization) {
	t.Helper()
	if _, err := j.NextProcessGeneration(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, err := j.BoundedDataIdentity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	authority.DataIdentity = &data
}

func TestBoundedRecoveryAdmissionReleaseKeepsRunProtectionAndHistoricalControls(t *testing.T) {
	r, as, path := retainedStrictFixture(t)
	r.cfg.InstanceID = "forced-candidate"
	r.cfg.UpgradeID = "forced-upgrade"
	r.cfg.InitialMaintenance = true
	r.bootstrapMaintenance = true
	authority := protocol.BoundedMaintenanceAuthorization{RequestID: "original-force", Role: "runner", Target: protocol.UpgradeBinding{UpgradeID: r.cfg.UpgradeID, InstanceID: "updater", ProcessInstanceID: r.cfg.InstanceID, ArtifactSHA256: r.cfg.Build.BinarySHA256}}
	bindRunnerBoundedTestAuthority(t, r.cfg.Journal, &authority)
	if err := r.cfg.Journal.QuarantineBoundedRecovery(t.Context(), authority); err != nil {
		t.Fatal(err)
	}
	if err := r.loadBoundedRecovery(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.loadControl(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, requests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
	releasedData := *authority.DataIdentity
	releasedData.Version = journal.CurrentVersion
	release := &protocol.BoundedRunnerAdmissionRelease{RequestID: "independent-owner-release", RecoveryAuthorizationID: authority.RequestID, Successor: authority.Target, RunnerGeneration: r.generation, HostID: r.cfg.HostID, Journal: releasedData, ExpectedControlRevision: r.control.Revision, Reason: "admit new work while retaining original unknown work"}
	command := protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerNormal, BoundedRecoveryRelease: release}
	if err := r.applyControl(t.Context(), &command, r.cfg.Now(), true); err != nil {
		t.Fatal(err)
	}
	if r.boundedRecoveryPending() || !r.boundedRunQuarantined(t.Context(), as.RunID) {
		t.Fatal("global release cleared original run isolation or kept global admission blocked")
	}
	if r.maintenance() {
		t.Fatal("validated owner release left the successor bootstrap fence blocking new work")
	}
	for _, verdict := range []string{protocol.VerdictContinue, protocol.VerdictStop} {
		retainedStrictHello(t, r, protocol.RunVerdict{RunID: as.RunID, Verdict: verdict, Desired: protocol.DesiredRun})
		if err := r.hello(t.Context()); err != nil {
			t.Fatal(err)
		}
		r.wg.Wait()
	}
	for _, mode := range []string{protocol.RunnerMaintenance, protocol.RunnerNormal} {
		command.Revision++
		command.Mode = mode
		if err := r.applyControl(t.Context(), &command, r.cfg.Now(), false); err != nil {
			t.Fatalf("historical release blocked ordinary %s control: %v", mode, err)
		}
	}
	if err := r.cfg.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := journal.Open(t.Context(), path, r.cfg.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	cfg := r.cfg
	cfg.Journal = j
	r = New(cfg)
	if err := r.loadBoundedRecovery(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.loadControl(t.Context()); err != nil {
		t.Fatal(err)
	}
	command.Revision++
	if err := r.applyControl(t.Context(), &command, r.cfg.Now(), true); err != nil {
		t.Fatalf("historical release blocked a later process generation: %v", err)
	}
	if r.maintenance() {
		t.Fatal("persisted release failed to clear the restarted successor's bootstrap fence")
	}
	after, afterRequests := retainedStrictSnapshot(t, j, as.RunID)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(requests, afterRequests) {
		t.Fatal("admission release or ordinary control changed original unknown work")
	}
	second := authority
	second.RequestID = "second-force"
	if err := j.QuarantineBoundedRecovery(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if err := r.loadBoundedRecovery(t.Context()); err != nil {
		t.Fatal(err)
	}
	command.Revision++
	command.Mode = protocol.RunnerMaintenance
	if err := r.applyControl(t.Context(), &command, r.cfg.Now(), false); err != nil {
		t.Fatalf("old release blocked a second force window: %v", err)
	}
	if !r.boundedRecoveryPending() || !r.boundedRunQuarantined(t.Context(), as.RunID) {
		t.Fatal("old release authorized another recovery lineage")
	}
}
