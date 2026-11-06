package runner

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func boundedFinalFixture(t *testing.T, role protocol.RunRole, stopped bool) (*Runner, protocol.Assignment, string, journal.Request) {
	t.Helper()
	r, as, path := retainedStrictFixture(t)
	if err := r.cfg.Journal.DeleteRequest(t.Context(), "original-business-request"); err != nil {
		t.Fatal(err)
	}
	if role != as.Role {
		as.RunID, as.Role, as.LeaseID = "original-acceptance", role, "acceptance-lease"
		if err := r.cfg.Journal.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerOrca); err != nil {
			t.Fatal(err)
		}
		if err := r.cfg.Journal.SetThread(t.Context(), as.RunID, "original-acceptance-thread"); err != nil {
			t.Fatal(err)
		}
	}
	if stopped {
		if err := r.cfg.Journal.ConfirmStopped(t.Context(), as.RunID); err != nil {
			t.Fatal(err)
		}
	}
	operation := protocol.OpRunsSubmitArtifact
	if role == protocol.RoleAccept {
		operation = protocol.OpAcceptancesSubmit
	}
	if err := r.finish(t.Context(), as, journal.PhaseSubmitting, operation, json.RawMessage(`{"original":"result"}`)); err != nil {
		t.Fatal(err)
	}
	request, ok, err := r.cfg.Journal.RequestForRun(t.Context(), as.RunID)
	if err != nil || !ok {
		t.Fatal(request, ok, err)
	}
	authority := protocol.BoundedMaintenanceAuthorization{RequestID: "original-force", Role: "runner"}
	bindRunnerBoundedTestAuthority(t, r.cfg.Journal, &authority)
	if err := r.cfg.Journal.QuarantineBoundedRecovery(t.Context(), authority); err != nil {
		t.Fatal(err)
	}
	if err := r.loadBoundedRecovery(t.Context()); err != nil {
		t.Fatal(err)
	}
	return r, as, path, request
}

func TestBoundedFinalFactRequiresOriginalPhysicalStop(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		t.Run(string(role), func(t *testing.T) {
			r, as, _, original := boundedFinalFixture(t, role, false)
			before, requests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
			var sent []protocol.Envelope
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				_ = json.NewDecoder(q.Body).Decode(&env)
				sent = append(sent, env)
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}})
			}))
			defer server.Close()
			r.c.base, r.c.http = server.URL, server.Client()
			r.replayBoundedFacts(t.Context())
			after, remaining := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
			if len(sent) != 0 || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(requests, remaining) {
				t.Fatalf("unconfirmed predecessor's terminal fact %s escaped: sent=%+v run=%+v requests=%+v", original.Envelope.RequestID, sent, after, remaining)
			}
		})
	}
}

func TestBoundedFinalAcknowledgementSurvivesRestartAndSettlesOnlyOriginalRun(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		t.Run(string(role), func(t *testing.T) {
			r, as, path, original := boundedFinalFixture(t, role, true)
			unknown := as
			unknown.RunID, unknown.LeaseID = "different-unknown-run", "different-lease"
			if err := r.cfg.Journal.AcceptAssignmentFromWorker(t.Context(), unknown, "", protocol.WorkerOrca); err != nil {
				t.Fatal(err)
			}
			if err := r.cfg.Journal.SetHeld(t.Context(), unknown.RunID, true); err != nil {
				t.Fatal(err)
			}
			// A second frozen inventory includes both runs while retaining the
			// first authorization and the exact pending terminal request.
			authority := protocol.BoundedMaintenanceAuthorization{RequestID: "second-force", Role: "runner"}
			bindRunnerBoundedTestAuthority(t, r.cfg.Journal, &authority)
			if err := r.cfg.Journal.QuarantineBoundedRecovery(t.Context(), authority); err != nil {
				t.Fatal(err)
			}
			unknownBefore, _ := retainedStrictSnapshot(t, r.cfg.Journal, unknown.RunID)
			var mu sync.Mutex
			var received []protocol.Envelope
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				_ = json.NewDecoder(q.Body).Decode(&env)
				mu.Lock()
				received = append(received, env)
				mu.Unlock()
				var result any = map[string]any{}
				if env.Operation == protocol.OpRunnerHello {
					result = protocol.HelloResult{Runs: []protocol.RunVerdict{{RunID: as.RunID, Verdict: protocol.VerdictStop, Desired: protocol.DesiredStopped}, {RunID: unknown.RunID, Verdict: protocol.VerdictStop, Desired: protocol.DesiredStopped}}}
				}
				body, _ := json.Marshal(result)
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: body}})
			}))
			defer server.Close()
			r.c.base, r.c.http = server.URL, server.Client()
			r.replayBoundedFacts(t.Context())
			if permit, err := r.cfg.Journal.RetainedResume(t.Context(), as.RunID); err != nil || permit != nil || !r.boundedRunQuarantined(t.Context(), as.RunID) {
				t.Fatal("terminal ACK granted attachment or cleared quarantine", permit, err)
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
				t.Fatal("durable successful terminal fact did not settle after restart", err)
			}
			unknownAfter, _ := retainedStrictSnapshot(t, j, unknown.RunID)
			if !reflect.DeepEqual(unknownBefore, unknownAfter) || !r.boundedRunQuarantined(t.Context(), unknown.RunID) || !r.boundedRecoveryPending() {
				t.Fatal("settling one known result changed another unknown or global admission", unknownAfter)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(received) != 2 || !reflect.DeepEqual(received[0], original.Envelope) || received[1].Operation != protocol.OpRunnerHello {
				t.Fatalf("recovery repeated original terminal/native work or changed envelope: %+v", received)
			}
		})
	}
}

func TestBoundedFinalStopRequiresSuccessfulMatchingAcknowledgement(t *testing.T) {
	for _, scenario := range []string{"accepted", "failed", "wrong-request", "missing-ack", "continue", "stop-with-run-desired", "accepted-hello"} {
		t.Run(scenario, func(t *testing.T) {
			r, as, _, original := boundedFinalFixture(t, protocol.RoleImplement, true)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				_ = json.NewDecoder(q.Body).Decode(&env)
				op := protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}
				if env.Operation == protocol.OpRunnerHello {
					v := protocol.RunVerdict{RunID: as.RunID, Verdict: protocol.VerdictStop, Desired: protocol.DesiredStopped}
					if scenario == "continue" {
						v.Verdict = protocol.VerdictContinue
					}
					if scenario == "stop-with-run-desired" {
						v.Desired = protocol.DesiredRun
					}
					if scenario == "accepted-hello" {
						op.Status = protocol.OpAccepted
					}
					op.Result, _ = json.Marshal(protocol.HelloResult{Runs: []protocol.RunVerdict{v}})
				} else {
					switch scenario {
					case "accepted":
						op.Status = protocol.OpAccepted
					case "failed":
						op.Status, op.Error = protocol.OpFailed, protocol.Errorf(protocol.CodeForbidden, "original result remains unknown")
					case "wrong-request":
						op.RequestID = "different-request"
					}
				}
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &op})
			}))
			defer server.Close()
			r.c.base, r.c.http = server.URL, server.Client()
			if scenario != "missing-ack" {
				r.replayBoundedFacts(t.Context())
			} else if err := r.cfg.Journal.DeleteRequest(t.Context(), original.Envelope.RequestID); err != nil {
				t.Fatal(err)
			}
			if err := r.hello(t.Context()); err != nil {
				t.Fatal(err)
			}
			run, err := r.cfg.Journal.Run(t.Context(), as.RunID)
			if err != nil || !run.Fenced || !run.StoppedConfirmed || !r.boundedRunQuarantined(t.Context(), as.RunID) {
				t.Fatal("incomplete terminal evidence released original run", run, err)
			}
			if scenario == "accepted" || scenario == "failed" || scenario == "wrong-request" {
				request, ok, err := r.cfg.Journal.RequestForRun(t.Context(), as.RunID)
				if err != nil || !ok || !reflect.DeepEqual(request, original) {
					t.Fatal("unsettled acknowledgement lost original envelope", request, ok, err)
				}
			}
			if probe := r.cfg.Workspaces.(*retainedStrictWorkspaceProbe); probe.finishes.Load() != 0 || probe.prepares.Load() != 0 || probe.reattaches.Load() != 0 {
				t.Fatal("incomplete terminal evidence invoked workspace/native recovery")
			}
		})
	}
}
