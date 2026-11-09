package runner

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func TestBoundedManagerOriginalCloseAndStoppedFactsSettleWithoutNative(t *testing.T) {
	for _, releaseBefore := range []bool{false, true} {
		t.Run(strconv.FormatBool(releaseBefore), func(t *testing.T) { boundedManagerFinalScenario(t, releaseBefore, false) })
	}
}

func TestFault_BoundedManagerStopPreservesUnacknowledgedClose(t *testing.T) {
	for _, releaseBefore := range []bool{false, true} {
		t.Run(strconv.FormatBool(releaseBefore), func(t *testing.T) { boundedManagerFinalScenario(t, releaseBefore, true) })
	}
}

func boundedManagerFinalScenario(t *testing.T, releaseBefore, closePending bool) {
	t.Helper()
	r, unknown, path := retainedStrictFixture(t)
	r.cfg.InstanceID, r.cfg.UpgradeID, r.cfg.HostID = "replacement-runner", "original-upgrade", "original-host"
	r.cfg.Build.BinarySHA256 = strings.Repeat("a", 64)
	if err := r.cfg.Journal.SetHeld(t.Context(), unknown.RunID, true); err != nil {
		t.Fatal(err)
	}
	as := protocol.Assignment{Protocol: protocol.CurrentRunnerProtocol, RunID: "original-manager", LeaseID: "original-manager-lease", Role: protocol.RoleIssueManager, Manager: &protocol.IssueManagerAssignment{ConversationID: "original-conversation"}}
	if err := r.cfg.Journal.AcceptAssignment(t.Context(), as, ""); err != nil {
		t.Fatal(err)
	}
	var events []protocol.Envelope
	for sequence := 1; sequence <= 86; sequence++ {
		env := protocol.Envelope{RequestID: "original-event-" + strconv.Itoa(sequence), Operation: protocol.OpManagerNativeEvent, Target: runTarget(as)}
		if err := r.cfg.Journal.SaveManagerEvent(t.Context(), as.RunID, &env, json.RawMessage(`{"original":"event"}`)); err != nil {
			t.Fatal(err)
		}
		if sequence <= 82 {
			if err := r.cfg.Journal.DeleteRequest(t.Context(), env.RequestID); err != nil {
				t.Fatal(err)
			}
		} else {
			events = append(events, env)
		}
	}
	command := protocol.ManagerNativeRequest{ID: "original-native-close", Method: "close", Payload: json.RawMessage(`{}`)}
	if _, _, err := r.cfg.Journal.ManagerOperation(t.Context(), as.RunID, command); err != nil {
		t.Fatal(err)
	}
	reply := protocol.ManagerNativeReply{ID: command.ID, LastSequence: 86, Result: json.RawMessage(`null`)}
	if err := r.cfg.Journal.CompleteManagerOperation(t.Context(), reply); err != nil {
		t.Fatal(err)
	}
	closeBody, _ := json.Marshal(reply)
	closeEnvelope := protocol.Envelope{RequestID: "original-close-envelope", Operation: protocol.OpManagerNativeReply, Target: runTarget(as), Payload: closeBody}
	if err := r.cfg.Journal.PutRequest(t.Context(), journal.Request{RunID: as.RunID, Envelope: closeEnvelope}); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.ConfirmStopped(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	if err := r.finish(t.Context(), as, journal.PhaseReleasing, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "original native process stopped", LastSequence: 86}); err != nil {
		t.Fatal(err)
	}
	authority := protocol.BoundedMaintenanceAuthorization{RequestID: "original-force", Role: "runner", Target: protocol.UpgradeBinding{UpgradeID: r.cfg.UpgradeID, ProcessInstanceID: r.cfg.InstanceID, ArtifactSHA256: r.cfg.Build.BinarySHA256}}
	bindRunnerBoundedTestAuthority(t, r.cfg.Journal, &authority)
	if err := r.cfg.Journal.QuarantineBoundedRecovery(t.Context(), authority); err != nil {
		t.Fatal(err)
	}
	if err := r.loadBoundedRecovery(t.Context()); err != nil {
		t.Fatal(err)
	}
	unknownBefore, pending := retainedStrictSnapshot(t, r.cfg.Journal, unknown.RunID)
	var newNormal protocol.Assignment
	if releaseBefore {
		if err := r.loadControl(t.Context()); err != nil {
			t.Fatal(err)
		}
		release := protocol.BoundedRunnerAdmissionRelease{RequestID: "fresh-owner-release", RecoveryAuthorizationID: authority.RequestID, Successor: authority.Target,
			RunnerGeneration: r.generation, HostID: r.cfg.HostID, Journal: *authority.DataIdentity, ExpectedControlRevision: r.control.Revision, Reason: "admit new work while original manager facts reconcile"}
		if err := r.applyControl(t.Context(), &protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerNormal, BoundedRecoveryRelease: &release}, r.cfg.Now(), true); err != nil {
			t.Fatal(err)
		}
		newNormal = protocol.Assignment{Protocol: protocol.CurrentRunnerProtocol, RunID: "new-normal-run", LeaseID: "new-normal-lease", Role: protocol.RoleImplement}
		if err := r.cfg.Journal.AcceptAssignment(t.Context(), newNormal, ""); err != nil {
			t.Fatal(err)
		}
		if err := r.cfg.Journal.ConfirmStopped(t.Context(), newNormal.RunID); err != nil {
			t.Fatal(err)
		}
		if err := r.finish(t.Context(), newNormal, journal.PhaseSubmitting, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{}); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	var received []protocol.Envelope
	stoppedReceived := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		_ = json.NewDecoder(q.Body).Decode(&env)
		mu.Lock()
		defer mu.Unlock()
		received = append(received, env)
		if env.Operation == protocol.OpManagerNativeStopped {
			stoppedReceived = true
		}
		if env.Operation == protocol.OpManagerNativeReply && (!stoppedReceived || closePending) {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(rpcResponse{Error: protocol.Errorf(protocol.CodeUnknownOutcome, "original business receipt remains unknown")})
			return
		}
		var body json.RawMessage = json.RawMessage(`{}`)
		if env.Operation == protocol.OpRunnerHello {
			body, _ = json.Marshal(protocol.HelloResult{Runs: []protocol.RunVerdict{{RunID: as.RunID, Verdict: protocol.VerdictStop, Desired: protocol.DesiredStopped}}})
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: body}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	if !releaseBefore {
		r.replayBoundedFacts(t.Context())
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
	r.c.base, r.c.http = server.URL, server.Client()
	if err := r.loadBoundedRecovery(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.hello(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Run(t.Context(), as.RunID); !errors.Is(err, journal.ErrNotFound) {
		t.Fatal("acknowledged original manager facts left its stopped run occupying a slot", err)
	}
	unknownAfter, remaining := retainedStrictSnapshot(t, j, unknown.RunID)
	if !reflect.DeepEqual(unknownBefore, unknownAfter) || !r.boundedRunQuarantined(t.Context(), unknown.RunID) || r.boundedRecoveryPending() == releaseBefore {
		t.Fatal("manager terminal facts changed unrelated unknown work", unknownAfter)
	}
	wantPending := 1
	if releaseBefore {
		wantPending++
		if run, err := j.Run(t.Context(), newNormal.RunID); err != nil || run.Phase != journal.PhaseSubmitting || !run.StoppedConfirmed || r.boundedRunQuarantined(t.Context(), newNormal.RunID) {
			t.Fatal("old factual replay consumed or quarantined a new normal run", run, err)
		}
	}
	if len(remaining) != wantPending || len(pending) != 7 {
		t.Fatal("manager receipt reconciliation changed unrelated pending facts", remaining)
	}
	mu.Lock()
	defer mu.Unlock()
	wantReceived := 7
	if closePending && !releaseBefore {
		wantReceived++ // A restart retries the still-unacknowledged close.
	}
	if len(received) != wantReceived {
		t.Fatal("manager recovery created a new request or native call", received)
	}
	for i := range events {
		if !reflect.DeepEqual(events[i], received[i]) {
			t.Fatal("manager event prefix changed", events[i], received[i])
		}
	}
	if received[4].Operation != protocol.OpManagerNativeStopped || !reflect.DeepEqual(received[5], closeEnvelope) || received[len(received)-1].Operation != protocol.OpRunnerHello {
		t.Fatal("manager receipt identities or order changed", received)
	}
	if wantReceived == 8 && !reflect.DeepEqual(received[6], closeEnvelope) {
		t.Fatal("restart changed the original pending close", received[6])
	}
	if closePending {
		db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var raw string
		if err := db.QueryRowContext(t.Context(), `SELECT authority_json FROM retained_run_resumes WHERE run_id=?`, as.RunID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var history struct {
			Ack struct {
				Envelope protocol.Envelope  `json:"envelope"`
				Close    *protocol.Envelope `json:"manager_close_envelope"`
			} `json:"bounded_final_ack"`
			StopHistory struct {
				Requests []struct {
					ID        string `json:"request_id"`
					Operation string `json:"operation"`
					Payload   string `json:"payload_json"`
				} `json:"requests"`
			} `json:"stop_history"`
		}
		if err := json.Unmarshal([]byte(raw), &history); err != nil {
			t.Fatal(err)
		}
		if history.Ack.Envelope.Operation != protocol.OpManagerNativeStopped || history.Ack.Close != nil || len(history.StopHistory.Requests) != 1 {
			t.Fatal("physical Stop forged a close ACK or lost its pending history", raw)
		}
		p := history.StopHistory.Requests[0]
		if p.ID != closeEnvelope.RequestID || p.Operation != closeEnvelope.Operation || p.Payload != string(closeEnvelope.Payload) {
			t.Fatal("independent Stop changed the original unacknowledged close", raw)
		}
	}
	if probe := cfg.Workspaces.(*retainedStrictWorkspaceProbe); probe.finishes.Load() != 0 || probe.prepares.Load() != 0 || probe.reattaches.Load() != 0 {
		t.Fatal("manager settlement invoked a workspace or native continuation")
	}
}
