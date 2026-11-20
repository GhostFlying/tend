package app

import (
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestDelegatedDelayedRunningCannotEraseStop(t *testing.T) {
	h := newHarness(t)
	_, task := h.createTask(owner, createTaskPayload{Title: "offline stop", Spec: testSpec})
	as, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no claim")
	}
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	h.runAs(as, protocol.OpRunsRecordSessionStop, protocol.SessionStopPayload{PhysicalExit: true, EventSequence: 3}, RecordSessionStop{})
	_, err := h.app.RecordRunEvents(t.Context(), runPrincipal(as.RunID, as.Role), as.RunID, []protocol.RunEvent{{Seq: 2, Kind: protocol.EventRunRunning}})
	if err != nil {
		t.Fatal(err)
	}
	err = h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		d, err := views.Task(t.Context(), tx.Q(), task.TaskID)
		if err == nil && d.Runs[0].Observation.StoppedAt == 0 {
			t.Error("old running event erased later physical exit receipt")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDelegatedMaintenanceDeadlineRetainsWorkAndAllowsOtherHosts(t *testing.T) {
	h := newHarness(t)
	h.withMac()
	h.createTask(owner, createTaskPayload{Title: "original assigned task", Spec: testSpec})
	as, ok, _ := h.claim("linux-1")
	if !ok || !protocol.RunnerOwnsExecution(as.Protocol) {
		t.Fatal("missing delegated Run", as)
	}
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	deadline := h.clock.Now().Add(2 * DefaultLeaseTTL)
	ctl := protocol.RunnerControl{Mode: protocol.RunnerMaintenance, Revision: 1, Authorization: &protocol.MaintenanceAuthorization{ID: "test-grant", DeadlineMillis: deadline.UnixMilli(), RunIDs: []string{as.RunID}}}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.PutHostControl(t.Context(), "linux-1", ctl, h.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(DefaultLeaseTTL + time.Second)
	if expired, err := h.app.ExpireLeases(t.Context()); err != nil || expired != 0 {
		t.Fatal("ordinary timeout stopped covered work", expired, err)
	}
	h.clock.Advance(DefaultLeaseTTL)
	if expired, err := h.app.ExpireLeases(t.Context()); err != nil || expired != 1 {
		t.Fatal("explicit maintenance cutoff did not fence the original", expired, err)
	}
	if l := h.lease(as.RunID); l.Status != store.LeaseLapsed || l.Current(h.clock.Now().UnixMilli()) {
		t.Fatal("expired maintenance execution was renewed", l)
	}
	hs := h.hello("linux-1", protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunRunning})
	if len(hs.Runs) != 1 || hs.Runs[0].Verdict != protocol.VerdictRetain {
		t.Fatal("hello revived an unconfirmed writer", hs)
	}
	_, other := h.createTask(owner, createTaskPayload{Title: "independent task", Spec: testSpec})
	next, ok, _ := h.claim("mac-1")
	if !ok || next.TaskID != other.TaskID || next.Workspace.ID == as.Workspace.ID {
		t.Fatal("independent work could not proceed", next, other)
	}
}
