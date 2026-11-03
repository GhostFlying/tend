//go:build linux

package updater

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

type receiptCrashProcess struct {
	*fakeProcess
	crashes int
}

func (p *receiptCrashProcess) Crash(_ context.Context, id Identity, _ time.Duration) error {
	actual, ok := p.live[id.PID]
	if !ok {
		return nil
	}
	if actual != id {
		return ErrUnknown
	}
	p.crashes++
	delete(p.live, id.PID)
	return nil
}

func receiptRecoveryFixture(t *testing.T) (*harness, protocol.CommittedMaintenancePlan, *committedTestAuthority, *receiptCrashProcess, protocol.MaintenanceReceiptRecoveryRequest) {
	t.Helper()
	h, p, a := committedFixture(t)
	ids := protocol.NewULIDGen(time.Now, nil)
	p.ReceiptRecoveryStop = &protocol.CommittedReceiptRecoveryStop{Runs: []protocol.CommittedRetainedRun{}}
	for i := 0; i < 4; i++ {
		p.ReceiptRecoveryStop.Runs = append(p.ReceiptRecoveryStop.Runs, protocol.CommittedRetainedRun{TaskID: ids.New(protocol.PrefixTask), Run: protocol.RunView{RunID: ids.New(protocol.PrefixRun), Host: "fixture-host", Role: protocol.RoleIssueManager, Generation: 1, SpecVersion: 1, Observed: protocol.RunUnknown}})
	}
	h.s.state.Maintenance.Plan = p
	crash := &receiptCrashProcess{fakeProcess: h.p}
	h.s.opts.Process = crash
	a.archive.Blockers = []string{"native receipt reads: 7", "hosted operations are active or uncertain"}
	if err := h.s.PrepareCommittedMaintenance(t.Context(), p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	r := protocol.MaintenanceReceiptRecoveryRequest{RequestID: ids.New(protocol.PrefixRequest), ExpectedGeneration: h.s.Status().Generation, FirstCapture: p.ReceiptRecoveryStop.Runs, SecondCapture: p.ReceiptRecoveryStop.Runs}
	return h, p, a, crash, r
}

func TestReceiptRecoveryStopExplicitCrashRetainsUnknownObservation(t *testing.T) {
	h, p, a, crash, r := receiptRecoveryFixture(t)
	old := h.s.Status()
	if err := h.s.AuthorizeCommittedMaintenance(t.Context(), p, old.Generation); err == nil {
		t.Fatal("normal authorization accepted crash plan")
	}
	if err := h.s.RecoverCommittedMaintenance(t.Context(), p, r); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RecoverCommittedMaintenance(t.Context(), p, r); err != nil {
		t.Fatal("original authorization replay", err)
	}
	continuation := protocol.MaintenanceContinuation{RequestID: r.RequestID, ExpectedGeneration: h.s.Status().Generation, WindowClaim: p.WindowClaim, ExpiresAtMillis: p.ExpiresAtMillis, ResourceEvidence: p.ResourceEvidence, ResourceSHA256: p.ResourceSHA256}
	if err := h.s.ContinueCommittedMaintenance(t.Context(), p, continuation); err == nil || !strings.Contains(err.Error(), "invalid or stale maintenance continuation") {
		t.Fatal("stop authorization reused as a different continuation request", err)
	}
	changed := r
	changed.SecondCapture = nil
	if err := h.s.RecoverCommittedMaintenance(t.Context(), p, changed); err == nil {
		t.Fatal("same identity accepted changed inventory")
	}
	for i := 0; i < 7; i++ {
		h.step(t)
	}
	st := h.s.Status()
	if crash.crashes != 1 || h.p.stops != 0 || st.Maintenance.Phase != "successor_committed" || st.Maintenance.ArchiveAuthorization == nil || st.Maintenance.ArchiveAuthorization.HostedDrained || !st.Maintenance.ReceiptRecoveryAuthorized || st.Maintenance.ReceiptRecoveryRequestID != r.RequestID || a.archive.Hosts[0].OpenRuns != 4 {
		t.Fatal("crash recovery lost truthful source or signalled a graceful close", st.Maintenance, crash.crashes, h.p.stops)
	}
	if !reflect.DeepEqual(st.Maintenance.Plan.ReceiptRecoveryStop, p.ReceiptRecoveryStop) || !reflect.DeepEqual(old.CommittedProof, &st.Maintenance.OriginalProof) {
		t.Fatal("original inventory or proof rewritten")
	}
	if err := h.s.LinkCommittedMaintenance(*st.Maintenance.Receipt, st.Generation); err != nil {
		t.Fatal(err)
	}
	if err := h.s.FinishCommittedMaintenance(t.Context(), p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	if a.archive.Preparation.Prepared {
		t.Fatal("owned archive gate not released")
	}
}

func TestReceiptRecoveryStopRejectsIncompleteOrUnsafeFacts(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*protocol.CommittedMaintenancePlan, *protocol.ArchiveOnlineStatus, *protocol.MaintenanceReceiptRecoveryRequest)
	}{
		{"changed prepared captures", func(_ *protocol.CommittedMaintenancePlan, _ *protocol.ArchiveOnlineStatus, r *protocol.MaintenanceReceiptRecoveryRequest) {
			r.SecondCapture = nil
		}},
		{"incomplete inventory", func(p *protocol.CommittedMaintenancePlan, _ *protocol.ArchiveOnlineStatus, r *protocol.MaintenanceReceiptRecoveryRequest) {
			p.ReceiptRecoveryStop.Runs = p.ReceiptRecoveryStop.Runs[:3]
			r.FirstCapture = p.ReceiptRecoveryStop.Runs
			r.SecondCapture = r.FirstCapture
		}},
		{"native turn", func(_ *protocol.CommittedMaintenancePlan, a *protocol.ArchiveOnlineStatus, _ *protocol.MaintenanceReceiptRecoveryRequest) {
			a.Blockers = append(a.Blockers, "hosted native turns: 1")
		}},
		{"unknown effect", func(_ *protocol.CommittedMaintenancePlan, a *protocol.ArchiveOnlineStatus, _ *protocol.MaintenanceReceiptRecoveryRequest) {
			a.Blockers = append(a.Blockers, "new unrecognized effect: 1")
		}},
		{"no receipt uncertainty", func(_ *protocol.CommittedMaintenancePlan, a *protocol.ArchiveOnlineStatus, _ *protocol.MaintenanceReceiptRecoveryRequest) {
			a.Blockers = []string{"hosted operations are active or uncertain"}
		}},
		{"unprepared", func(_ *protocol.CommittedMaintenancePlan, a *protocol.ArchiveOnlineStatus, _ *protocol.MaintenanceReceiptRecoveryRequest) {
			a.Preparation.Prepared = false
		}},
		{"owner active", func(_ *protocol.CommittedMaintenancePlan, a *protocol.ArchiveOnlineStatus, _ *protocol.MaintenanceReceiptRecoveryRequest) {
			a.ActiveOwnerRequests = 1
		}},
		{"host not acknowledged", func(_ *protocol.CommittedMaintenancePlan, a *protocol.ArchiveOnlineStatus, _ *protocol.MaintenanceReceiptRecoveryRequest) {
			a.Hosts[0].Runtime.AppliedControlRevision--
		}},
		{"new pending claim", func(_ *protocol.CommittedMaintenancePlan, a *protocol.ArchiveOnlineStatus, _ *protocol.MaintenanceReceiptRecoveryRequest) {
			a.Hosts[0].Runtime.PendingClaims = 1
		}},
		{"stale generation", func(_ *protocol.CommittedMaintenancePlan, _ *protocol.ArchiveOnlineStatus, r *protocol.MaintenanceReceiptRecoveryRequest) {
			r.ExpectedGeneration--
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, p, a, crash, r := receiptRecoveryFixture(t)
			c.mutate(&p, &a.archive, &r)
			h.s.state.Maintenance.Plan = p
			if err := h.s.RecoverCommittedMaintenance(t.Context(), p, r); err == nil {
				t.Fatal("unsafe recovery accepted")
			}
			if crash.crashes != 0 || h.p.stops != 0 || h.s.Status().Maintenance.Phase != "updater_adopted" {
				t.Fatal("rejected recovery changed physical process")
			}
		})
	}
}

func TestReceiptRecoveryStopRechecksFactsBeforeCrash(t *testing.T) {
	h, p, a, crash, r := receiptRecoveryFixture(t)
	if err := h.s.RecoverCommittedMaintenance(t.Context(), p, r); err != nil {
		t.Fatal(err)
	}
	a.archive.ActiveOwnerRequests = 1
	if err := h.s.Step(t.Context()); err == nil || crash.crashes != 0 || h.p.stops != 0 {
		t.Fatal("drifted owner admission accepted crash")
	}
}

func TestReceiptRecoveryCapturesUseStableExecutionIdentity(t *testing.T) {
	h, p, a, _, r := receiptRecoveryFixture(t)
	// A normal-mode plan and two prepared observations have independent full bytes.
	p.ReceiptRecoveryStop.Runs = append([]protocol.CommittedRetainedRun(nil), p.ReceiptRecoveryStop.Runs...)
	p.ReceiptRecoveryStop.Runs[0].Run.Observed = protocol.RunRunning
	h.s.state.Maintenance.Plan = p
	r.FirstCapture = append([]protocol.CommittedRetainedRun(nil), r.FirstCapture...)
	r.SecondCapture = append([]protocol.CommittedRetainedRun(nil), r.SecondCapture...)
	r.FirstCapture[0].Run.Preparation = &protocol.RunPreparationFailure{Phase: protocol.PreparationPhaseWorkspace}
	r.SecondCapture[0].Run.Preparation = &protocol.RunPreparationFailure{Phase: protocol.PreparationPhaseWorkspace, Code: "observation_changed"}
	// Input order is not execution identity either.
	r.SecondCapture[0], r.SecondCapture[3] = r.SecondCapture[3], r.SecondCapture[0]
	a.archive.HostedDrained = true
	a.archive.Blockers = []string{"native receipt reads: 7"}
	if err := h.s.RecoverCommittedMaintenance(t.Context(), p, r); err != nil {
		t.Fatal(err)
	}
	st := h.s.Status().Maintenance
	if st.ReceiptRecoveryFirstSHA256 != digest(r.FirstCapture) || st.ReceiptRecoverySecondSHA256 != digest(r.SecondCapture) || st.ReceiptRecoveryFirstSHA256 == st.ReceiptRecoverySecondSHA256 || !st.ArchiveAuthorization.HostedDrained {
		t.Fatal("original snapshots or truthful drain observation were rewritten", st)
	}
	if err := h.s.RecoverCommittedMaintenance(t.Context(), p, r); err != nil {
		t.Fatal(err)
	}
	changed := r
	changed.SecondCapture = append([]protocol.CommittedRetainedRun(nil), r.SecondCapture...)
	changed.SecondCapture[0].Run.Observed = protocol.RunWaiting
	if err := h.s.RecoverCommittedMaintenance(t.Context(), p, changed); err == nil {
		t.Fatal("request replay accepted changed observation bytes")
	}
	for i := 0; i < 7; i++ {
		h.step(t)
	}
	if h.s.Status().Maintenance.Phase != "successor_committed" {
		t.Fatal(h.s.Status().Maintenance)
	}
}

func TestReceiptRecoveryCapturesRejectChangedExecutionIdentity(t *testing.T) {
	_, p, _, _, _ := receiptRecoveryFixture(t)
	cases := map[string]func([]protocol.CommittedRetainedRun) []protocol.CommittedRetainedRun{
		"missing":   func(v []protocol.CommittedRetainedRun) []protocol.CommittedRetainedRun { return v[:3] },
		"duplicate": func(v []protocol.CommittedRetainedRun) []protocol.CommittedRetainedRun { return append(v, v[0]) },
		"new": func(v []protocol.CommittedRetainedRun) []protocol.CommittedRetainedRun {
			v[0].Run.RunID = "run_01M6F01JSS6Q9BFV6DJNSSBHF9"
			return v
		},
		"task": func(v []protocol.CommittedRetainedRun) []protocol.CommittedRetainedRun {
			v[0].TaskID = "task_01M6F01JSS6Q9BFV6DJNSSBHF9"
			return v
		},
		"role": func(v []protocol.CommittedRetainedRun) []protocol.CommittedRetainedRun {
			v[0].Run.Role = protocol.RoleImplement
			return v
		},
		"generation": func(v []protocol.CommittedRetainedRun) []protocol.CommittedRetainedRun {
			v[0].Run.Generation++
			return v
		},
		"spec": func(v []protocol.CommittedRetainedRun) []protocol.CommittedRetainedRun {
			v[0].Run.SpecVersion++
			return v
		},
		"host": func(v []protocol.CommittedRetainedRun) []protocol.CommittedRetainedRun {
			v[0].Run.Host = "other-fixture"
			return v
		},
		"terminal": func(v []protocol.CommittedRetainedRun) []protocol.CommittedRetainedRun {
			v[0].Run.Observed = protocol.RunFinished
			return v
		},
		"invalid_role": func(v []protocol.CommittedRetainedRun) []protocol.CommittedRetainedRun {
			v[0].Run.Role = "invalid"
			return v
		},
		"zero_generation": func(v []protocol.CommittedRetainedRun) []protocol.CommittedRetainedRun {
			v[0].Run.Generation = 0
			return v
		},
		"zero_spec": func(v []protocol.CommittedRetainedRun) []protocol.CommittedRetainedRun {
			v[0].Run.SpecVersion = 0
			return v
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			changed := mutate(append([]protocol.CommittedRetainedRun(nil), p.ReceiptRecoveryStop.Runs...))
			if err := ValidateReceiptRecoveryCaptures(p, p.ReceiptRecoveryStop.Runs, changed); err == nil {
				t.Fatal("changed inventory accepted")
			}
		})
	}
}

func TestReceiptRecoveryDrainedSummaryHistoryPreservesAuthorization(t *testing.T) {
	h, p, a, crash, r := receiptRecoveryFixture(t)
	a.archive.HostedDrained = true
	a.archive.Blockers = []string{"native receipt reads: 7", "summary operations: 2"}
	original := a.archive
	if err := h.s.RecoverCommittedMaintenance(t.Context(), p, r); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*h.s.Status().Maintenance.ArchiveAuthorization, original) {
		t.Fatal("historical summary observation was rewritten")
	}
	// The independent live drain must still hold at the actual stop boundary.
	a.archive.HostedDrained = false
	for i := 0; i < 2; i++ {
		if err := h.s.Step(t.Context()); err == nil {
			t.Fatal("summary history accepted without live drain")
		}
	}
	if crash.crashes != 0 || h.p.stops != 0 {
		t.Fatal("drain drift stopped the previous server")
	}
	a.archive.HostedDrained = true
	for i := 0; i < 7; i++ {
		h.step(t)
	}
	if st := h.s.Status(); st.Maintenance.Phase != "successor_committed" || crash.crashes != 1 || !reflect.DeepEqual(*st.Maintenance.ArchiveAuthorization, original) {
		t.Fatal("drained history did not preserve the authorized transition", st.Maintenance)
	}
}

func TestReceiptRecoveryDrainedSummaryHistoryRejectsUnsafeFacts(t *testing.T) {
	for _, tc := range []struct {
		name     string
		drained  bool
		blockers []string
	}{
		{"not drained", false, []string{"summary operations: 2"}},
		{"malformed", true, []string{"summary operations: unknown"}},
		{"zero", true, []string{"summary operations: 0"}},
		{"negative", true, []string{"summary operations: -1"}},
		{"overflow", true, []string{"summary operations: 9223372036854775808"}},
		{"duplicate", true, []string{"summary operations: 2", "summary operations: 2"}},
		{"other native effect", true, []string{"summary operations: 2", "hosted native turns: 1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, p, a, crash, r := receiptRecoveryFixture(t)
			a.archive.HostedDrained = tc.drained
			a.archive.Blockers = append([]string{"native receipt reads: 7"}, tc.blockers...)
			if err := h.s.RecoverCommittedMaintenance(t.Context(), p, r); err == nil {
				t.Fatal("unsafe history accepted")
			}
			if crash.crashes != 0 || h.p.stops != 0 || h.s.Status().Maintenance.Phase != "updater_adopted" {
				t.Fatal("rejection changed the physical process")
			}
		})
	}
}
