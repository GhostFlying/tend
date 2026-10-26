package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func observationFixture(t *testing.T, role protocol.RunRole) (*harness, protocol.Assignment, string) {
	t.Helper()
	h := newHarness(t)
	rt := testRuntime("observation-host")
	rt.Capabilities = append(rt.Capabilities, protocol.CapabilityWorkerObservation)
	if op := h.versionedHello("linux-1", rt); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	plan := workPlan()
	plan.Intent = "PRIVATE_IMPLEMENTATION_DESIGN"
	plan.FollowOns = []protocol.WorkItemFollowOn{{FollowOnID: "follow", Kind: "commitment", Text: "PRIVATE_FOLLOW_ON", Source: "owner", Status: "open"}}
	w := h.createWork(plan)
	other := workPlan()
	other.Title = "OTHER_PRIVATE_ITEM"
	h.createWork(other)
	spec := testSpec
	spec.Observation = &protocol.ObservationScope{WorkItemIDs: []string{w.WorkItemID}, Deployment: true}
	op, task := h.createTask(owner, createTaskPayload{Title: "PRIVATE_TASK_TITLE", Spec: spec, WorkItemID: w.WorkItemID, WorkItemRevision: w.Revision, StageIDs: []string{"investigate"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as, ok := h.claimInputRun(rt)
	if !ok {
		t.Fatal("no claim")
	}
	if role == protocol.RoleAccept {
		h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
		if op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		as, ok = h.claimInputRun(rt)
		if !ok || as.Role != role {
			t.Fatal(as)
		}
	}
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	h.app.observeDeployment = func(context.Context) (protocol.ObservedDeployment, error) {
		return protocol.ObservedDeployment{ActualServer: &protocol.ObservedServer{BinarySHA256: strings.Repeat("a", 64), ProcessInstanceID: "actual", PID: 123}, ProofStatus: "unavailable"}, nil
	}
	return h, as, w.WorkItemID
}

func TestRunObservationScopeAndFreshness(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		t.Run(string(role), func(t *testing.T) {
			h, as, id := observationFixture(t, role)
			p := runPrincipal(as.RunID, role)
			first, err := h.app.ReadRunObservation(t.Context(), p)
			if err != nil {
				t.Fatal(err)
			}
			if first.Version != 1 || first.SpecVersion != 1 || first.RunID != as.RunID || len(first.WorkItems) != 1 || first.WorkItems[0].WorkItemID != id {
				t.Fatalf("identity: %+v", first)
			}
			w := first.WorkItems[0]
			if len(w.FollowOns) != 1 || w.FollowOns[0].FollowOnID != "follow" || w.Execution == nil || len(w.StageExecution) != 1 || len(w.Tasks) != 1 || len(w.Tasks[0].Runs) == 0 {
				t.Fatalf("missing execution facts: %+v", w)
			}
			if first.Deployment.SchemaVersion == 0 || first.Deployment.ActualServer.PID != 123 || first.Deployment.CommittedProof != nil || first.Deployment.ProofStatus != "unavailable" {
				t.Fatal(first.Deployment)
			}
			if first.Deployment.Runner == nil || first.Deployment.Runner.HostID != "linux-1" || !first.Deployment.Runner.Online || first.Deployment.Runner.InstanceID != "observation-host" {
				t.Fatal("own host deployment projection missing", first.Deployment.Runner)
			}
			raw, _ := json.Marshal(first)
			if strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "background") || strings.Contains(string(raw), "report") {
				t.Fatalf("sensitive projection: %s", raw)
			}
			h.clock.Advance(time.Second)
			h.createWork(workPlan())
			next, err := h.app.ReadRunObservation(t.Context(), p)
			if err != nil || next.Seq <= first.Seq || next.ObservedAt <= first.ObservedAt {
				t.Fatalf("stale observation: %+v %v", next, err)
			}
		})
	}
}

func TestRunObservationRejectsInactiveIdentity(t *testing.T) {
	for _, change := range []string{"owner", "wrong_role", "expired", "released", "paused", "old_spec", "finished", "unknown", "no_scope"} {
		t.Run(change, func(t *testing.T) {
			h, as, _ := observationFixture(t, protocol.RoleImplement)
			p := runPrincipal(as.RunID, as.Role)
			switch change {
			case "owner":
				p = owner
			case "wrong_role":
				p.Role = protocol.RoleAccept
			case "expired":
				h.clock.Advance(24 * time.Hour)
			default:
				err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					var err error
					switch change {
					case "released":
						_, err = tx.Exec(t.Context(), `UPDATE leases SET status='released' WHERE run_id=?`, as.RunID)
					case "paused":
						_, err = tx.Exec(t.Context(), `UPDATE tasks SET control_mode='paused' WHERE task_id=?`, as.TaskID)
					case "old_spec":
						_, err = tx.Exec(t.Context(), `UPDATE tasks SET spec_version=2 WHERE task_id=?`, as.TaskID)
					case "unknown":
						_, err = tx.Exec(t.Context(), `UPDATE runs SET observed_state='unknown' WHERE run_id=?`, as.RunID)
					case "finished":
						_, err = tx.Exec(t.Context(), `UPDATE runs SET observed_state='finished' WHERE run_id=?`, as.RunID)
					case "no_scope":
						spec := testSpec
						body, _ := json.Marshal(spec)
						_, err = tx.Exec(t.Context(), `UPDATE task_specs SET body_json=? WHERE task_id=?`, string(body), as.TaskID)
					}
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.app.ReadRunObservation(t.Context(), p); err == nil {
				t.Fatal("unauthorized observation accepted")
			}
		})
	}
}

func TestRunObservationRechecksAfterProcessProbe(t *testing.T) {
	h, as, _ := observationFixture(t, protocol.RoleImplement)
	h.app.observeDeployment = func(context.Context) (protocol.ObservedDeployment, error) {
		h.clock.Advance(24 * time.Hour)
		return protocol.ObservedDeployment{}, nil
	}
	if _, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role)); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatalf("expired during probe: %v", err)
	}
}

func TestRunObservationWithoutDeploymentDoesNotProbe(t *testing.T) {
	h, as, _ := observationFixture(t, protocol.RoleImplement)
	spec := h.storedSpec(as.TaskID, 1)
	spec.Observation.Deployment = false
	body, _ := json.Marshal(spec)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE task_specs SET body_json=? WHERE task_id=?`, string(body), as.TaskID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h.app.observeDeployment = func(context.Context) (protocol.ObservedDeployment, error) {
		t.Fatal("unauthorized process probe")
		return protocol.ObservedDeployment{}, nil
	}
	result, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
	if err != nil || result.Deployment != nil || len(result.WorkItems) != 1 {
		t.Fatalf("scope: %+v %v", result, err)
	}
}

func TestObservationScopeMustExist(t *testing.T) {
	h := newHarness(t)
	spec := testSpec
	spec.Observation = &protocol.ObservationScope{WorkItemIDs: []string{h.ids.New(protocol.PrefixWorkItem)}}
	op, _ := h.createTask(owner, createTaskPayload{Title: "missing scope", Spec: spec})
	expectWorkCode(t, op, protocol.CodeNotFound)
}

// A recovered worker starts before its journal's running event is flushed.
// Fencing must still deny reads until hello reestablishes the active lease.
func TestRunObservationAfterRecoveryBeforeRunningEvent(t *testing.T) {
	for _, state := range []protocol.RunObserved{protocol.RunStarting, protocol.RunRunning, protocol.RunWaiting, protocol.RunInterrupted} {
		t.Run(string(state), func(t *testing.T) {
			h, as, _ := observationFixture(t, protocol.RoleImplement)
			h.clock.Advance(DefaultLeaseTTL + time.Second)
			if _, err := h.app.ExpireLeases(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role)); protocol.CodeOf(err) != protocol.CodeForbidden {
				t.Fatalf("fenced run read: %v", err)
			}
			rt := testRuntime("observation-host")
			rt.Generation = 1
			rt.Sequence = 2
			rt.Capabilities = append(rt.Capabilities, protocol.CapabilityWorkerObservation)
			env := h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "linux-1"}, protocol.HelloPayload{Worker: h.worker("linux-1"), WorkspaceProtocol: 1, Runtime: rt, Held: []protocol.HeldRun{{RunID: as.RunID, LeaseID: as.LeaseID, Observed: state}}}, nil)
			op := h.exec(runner("linux-1"), env, Hello{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if h.lease(as.RunID).Status != store.LeaseActive {
				t.Fatal("hello did not reauthorize lease")
			}
			if _, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role)); err != nil {
				t.Fatalf("read after legal recovery (%s): %v", state, err)
			}
		})
	}
}
