package app

import (
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestBoundedRunnerSavedGiveUpSettlesOnIndependentOwnerCancel(t *testing.T) {
	h, as, plan := boundedRunnerPlanFixture(t)
	if op, _ := h.createTask(owner, createTaskPayload{Title: "Keep unrelated uncertain work", Spec: testSpec}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	other, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("second run not claimed")
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		for _, id := range []string{as.RunID, other.RunID} {
			if err := tx.UpdateRunObserved(t.Context(), id, protocol.RunUnknown, h.clock.Now().UnixMilli()); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	grant, err := h.app.AuthorizeRunnerBoundedMaintenance(t.Context(), owner, plan)
	if err != nil {
		t.Fatal(err)
	}
	beforeRun, beforeLease := h.run(as.RunID), h.lease(as.RunID)
	principal := runPrincipal(as.RunID, as.Role)
	// The reason makes no claim about a native process exit. This authenticated
	// give-up stays unclassified until an independent owner stops the task.
	giveUp := h.env(protocol.OpRunsRelease, runTarget(as.RunID), protocol.ReleasePayload{Reason: "transport outcome remains unknown"}, nil)
	saved, err := h.app.Execute(t.Context(), principal, giveUp, Release{})
	if err != nil || saved.Status != protocol.OpAccepted {
		t.Fatal(saved, err)
	}
	otherGiveUp := h.runAs(other, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "unrelated transport outcome remains unknown"}, Release{})
	if otherGiveUp.Status != protocol.OpAccepted {
		t.Fatal(otherGiveUp)
	}
	beforeFact := boundedSavedGiveUpFact(t, h, as.RunID, giveUp.RequestID)
	if !reflect.DeepEqual(beforeRun, h.run(as.RunID)) || !reflect.DeepEqual(beforeLease, h.lease(as.RunID)) {
		t.Fatal("give-up alone settled uncertain work")
	}
	otherRun, otherLease := h.run(other.RunID), h.lease(other.RunID)
	control := h.env("tasks.cancel", taskTarget(as.TaskID), struct{}{}, rev(h.task(as.TaskID).ControlRevision))
	stopped := h.exec(owner, control, CancelTask{})
	if stopped.Status != protocol.OpSucceeded || h.run(as.RunID).ObservedState != protocol.RunFailed || h.lease(as.RunID).Status != store.LeaseReleased {
		t.Fatal("independent owner stop did not settle the exact saved give-up", stopped, h.run(as.RunID), h.lease(as.RunID))
	}
	if !reflect.DeepEqual(otherRun, h.run(other.RunID)) || !reflect.DeepEqual(otherLease, h.lease(other.RunID)) {
		t.Fatal("owner stop settled another protected task")
	}
	if after := boundedSavedGiveUpFact(t, h, as.RunID, giveUp.RequestID); after != beforeFact {
		t.Fatal("owner stop rewrote the original unclassified fact", beforeFact, after)
	}
	replay, err := h.app.Execute(t.Context(), principal, giveUp, Release{})
	if err != nil || !reflect.DeepEqual(saved, replay) {
		t.Fatal("owner stop rewrote the original accepted give-up receipt", saved, replay, err)
	}
	if replay := h.exec(owner, control, CancelTask{}); !reflect.DeepEqual(stopped, replay) {
		t.Fatal("owner retry changed its original receipt", stopped, replay)
	}
	if again, err := h.app.RunnerBoundedMaintenance(t.Context(), plan.HostID, plan.Authorization.RequestID); err != nil || !reflect.DeepEqual(grant, again) {
		t.Fatal("owner stop rewrote the immutable replacement authority", again, err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var count int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM events WHERE object_type='run' AND object_id=? AND kind='run.maintenance_stop_resolved'`, as.RunID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatalf("owner retry repeated settlement: %d events", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBoundedRunnerOwnerCancelRequiresMatchingSavedFact(t *testing.T) {
	for _, mismatch := range []string{"missing", "maintenance", "lease"} {
		t.Run(mismatch, func(t *testing.T) {
			h, as, plan := boundedRunnerPlanFixture(t)
			if _, err := h.app.AuthorizeRunnerBoundedMaintenance(t.Context(), owner, plan); err != nil {
				t.Fatal(err)
			}
			beforeRun, beforeLease := h.run(as.RunID), h.lease(as.RunID)
			if mismatch != "missing" {
				maintenance, lease := "runner-force:"+plan.Authorization.RequestID, as.LeaseID
				if mismatch == "maintenance" {
					maintenance = "runner-force:" + h.ids.New(protocol.PrefixRequest)
				} else {
					lease = h.ids.New(protocol.PrefixLease)
				}
				// A historical fact for another authority or lease cannot prove
				// the current frozen execution gave up its resources.
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `INSERT INTO maintenance_physical_stops(request_domain,request_id,maintenance_id,run_id,lease_id,operation,payload_json,last_sequence,created_at) VALUES(?,?,?,?,?,?,?,-1,?)`, runPrincipal(as.RunID, as.Role).DedupDomain(), h.ids.New(protocol.PrefixRequest), maintenance, as.RunID, lease, protocol.OpRunsRelease, `{"reason":"historical unclassified give-up"}`, h.clock.Now().UnixMilli())
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			op := h.exec(owner, h.env("tasks.cancel", taskTarget(as.TaskID), struct{}{}, rev(h.task(as.TaskID).ControlRevision)), CancelTask{})
			if op.Status != protocol.OpSucceeded || h.run(as.RunID).ObservedState != beforeRun.ObservedState || !reflect.DeepEqual(h.lease(as.RunID), beforeLease) {
				t.Fatal("owner cancellation invented an original matching give-up", op, h.run(as.RunID), h.lease(as.RunID))
			}
		})
	}
}

func TestBoundedRunnerRejectedOwnerCancelDoesNotSettleSavedGiveUp(t *testing.T) {
	h, as, plan := boundedRunnerPlanFixture(t)
	if _, err := h.app.AuthorizeRunnerBoundedMaintenance(t.Context(), owner, plan); err != nil {
		t.Fatal(err)
	}
	if op := h.runAs(as, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "unknown transport outcome"}, Release{}); op.Status != protocol.OpAccepted {
		t.Fatal(op)
	}
	beforeRun, beforeLease := h.run(as.RunID), h.lease(as.RunID)
	op := h.exec(owner, h.env("tasks.cancel", taskTarget(as.TaskID), struct{}{}, rev(h.task(as.TaskID).ControlRevision+1)), CancelTask{})
	if op.Error == nil || op.Error.Code != protocol.CodeStaleRevision || !reflect.DeepEqual(beforeRun, h.run(as.RunID)) || !reflect.DeepEqual(beforeLease, h.lease(as.RunID)) {
		t.Fatal("rejected owner control settled the saved fact", op, h.run(as.RunID), h.lease(as.RunID))
	}
}

func boundedSavedGiveUpFact(t *testing.T, h *harness, run, request string) string {
	t.Helper()
	var fact string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT json_array(request_domain,request_id,maintenance_id,run_id,lease_id,operation,payload_json,last_sequence,created_at) FROM maintenance_physical_stops WHERE run_id=? AND request_id=?`, run, request).Scan(&fact)
	}); err != nil {
		t.Fatal(err)
	}
	return fact
}
