package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

func restartAuthorityFixture(t *testing.T, journal string, version int64) (*harness, StartUpgrade, *protocol.RunnerRuntime) {
	t.Helper()
	h := newHarness(t)
	rt := testRuntime("original")
	rt.Protocol, rt.ProtocolMin, rt.ProtocolMax = version, version, version
	rt.JournalID = journal
	rt.JournalVersion = 9
	rt.Capabilities = append(rt.Capabilities, capabilityUpdater)
	rt.Build.BinarySHA256 = strings.Repeat("a", 64)
	if op := h.versionedHello("linux-1", rt); op.Error != nil {
		t.Fatal(op.Error)
	}
	c := upgradeFixtureRelease(t, h)
	c.ServerCapabilities = []string{release.ServerCapabilityRunnerJournals}
	return h, c, rt
}

func claimRestartWork(t *testing.T, h *harness, rt *protocol.RunnerRuntime, title string) protocol.Assignment {
	t.Helper()
	h.createTask(owner, createTaskPayload{Title: title, Spec: testSpec})
	rt.Sequence++
	a, ok := h.claimInputRun(rt)
	if !ok {
		t.Fatal("no assignment for", title)
	}
	if op := h.runAs(a, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Error != nil {
		t.Fatal(op.Error)
	}
	return a
}

func restartTestBinding(c StartUpgrade, batch string) protocol.UpgradeBinding {
	return protocol.UpgradeBinding{UpgradeID: batch, ReleaseID: c.Manifest.ReleaseID,
		ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: c.ServerHash, InstanceID: c.ServerInstance, ProcessInstanceID: "replacement"}
}

func TestUpgradeKeepsHistoricalJournalFencesAndFixedExecution(t *testing.T) {
	ctx := t.Context()
	h, c, rt := restartAuthorityFixture(t, "original-journal", protocol.CurrentRunnerProtocol)
	history := claimRestartWork(t, h, rt, "history")
	// Hello no longer lists the original Run. This records uncertainty and
	// lapses its lease without reporting a stop, ending work or freeing a slot.
	rt.Sequence++
	if op := h.versionedHello("linux-1", rt); op.Error != nil {
		t.Fatal(op.Error)
	}
	live := claimRestartWork(t, h, rt, "fixed execution")
	oldLease, liveLease := h.lease(history.RunID), h.lease(live.RunID)
	if oldLease.Status != store.LeaseLapsed || h.run(history.RunID).ObservedState != protocol.RunUnknown {
		t.Fatal("missing preexisting unresolved execution", oldLease)
	}
	if w := h.resourceForTask(history.TaskID); w.OccupiedBy != history.TaskID {
		t.Fatal("fixture has no original workspace fence", w)
	}
	h.createTask(owner, createTaskPayload{Title: "third work", Spec: testSpec})
	if op := startPreparedTestUpgrade(h, "restart", c); op.Error != nil {
		t.Fatal(op.Error)
	}
	if v := upgradeTick(h, ServerObservation{MainDrained: true, BinarySHA256: rt.Build.BinarySHA256}); v.Phase != "server_ready" {
		t.Fatal(v)
	}
	if ctl := upgradeCtl(h, "linux-1"); ctl.Mode != protocol.RunnerMaintenance || ctl.Authorization != nil {
		t.Fatal("fixed execution and unresolved history acquired a coverage grant", ctl)
	}
	b := restartTestBinding(c, "restart")
	if grant, err := h.app.ServerUpgradeAuthorize(ctx, b); protocol.CodeOf(err) != protocol.CodeInvalidTransition || grant.Allowed {
		t.Fatal("restart without admission ACK", grant, err)
	}
	ack := upgradeAck(h, "linux-1", protocol.RunnerMaintenance)
	if grant, err := h.app.ServerUpgradeAuthorize(ctx, b); err != nil || !grant.Allowed {
		t.Fatal(grant, err)
	}
	// Recreate the App from the persisted ledger before handling successor
	// health and committed receipt. No old in-memory state is transferred.
	h.app = New(h.store, h.ids, h.clock.Now)
	o := ServerObservation{InstanceID: b.InstanceID, ProcessInstanceID: b.ProcessInstanceID, BinarySHA256: b.ArtifactSHA256,
		Healthy: true, MainDrained: true, UpdaterReceipt: committedServerForTest(b)}
	upgradeTick(h, o)
	if v := upgradeTick(h, o); v.Phase != "runners" {
		t.Fatal(v)
	}
	if got := h.lease(history.RunID); got != oldLease || h.run(history.RunID).ObservedState != protocol.RunUnknown {
		t.Fatal("history was renewed, replaced or finished", got)
	}
	if got := h.lease(live.RunID); got != liveLease {
		t.Fatal("server cutover rewrote the fixed execution's original lease", got)
	}
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		open, occupied, err := store.HostSlotCounts(ctx, tx.Q(), "linux-1")
		if err == nil && (open != 2 || occupied != 2) {
			t.Errorf("historical lease no longer holds capacity: open=%d occupied=%d", open, occupied)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w := h.resourceForTask(history.TaskID); w.OccupiedBy != history.TaskID {
		t.Fatal("restart released the original workspace", w)
	}
	ack = upgradeAck(h, "linux-1", protocol.RunnerNormal)
	ack.Sequence++
	if a, ok := h.claimInputRun(&ack); ok || h.count("runs") != 2 {
		t.Fatal("restart released a slot or allocated another Run", a)
	}
}

func TestUpgradeRefusesUnprovedRestartAuthority(t *testing.T) {
	for _, tc := range []struct {
		name string
		code protocol.Code
	}{
		{"unknown with an active lease", protocol.CodeInvalidTransition},
		{"lapsed with a conflicting grant", protocol.CodeConflictPayload},
		{"lapsed without an identified journal", protocol.CodeInvalidTransition},
		{"lapsed in an unregistered journal", protocol.CodeInvalidTransition},
		{"renewable Run missing from confirmed coverage", protocol.CodeInvalidTransition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			journal, version := "journal", protocol.CurrentRunnerProtocol
			if tc.name == "lapsed without an identified journal" {
				journal = ""
			}
			if tc.name == "renewable Run missing from confirmed coverage" {
				version = protocol.MinimumRunnerProtocol
			}
			h, c, rt := restartAuthorityFixture(t, journal, version)
			a := claimRestartWork(t, h, rt, tc.name)
			if op := startPreparedTestUpgrade(h, "guard", c); op.Error != nil {
				t.Fatal(op.Error)
			}
			o := ServerObservation{MainDrained: true, BinarySHA256: rt.Build.BinarySHA256}
			if tc.name == "renewable Run missing from confirmed coverage" {
				upgradeTick(h, o)
				ctl := upgradeCtl(h, "linux-1")
				ctl.Authorization = nil
				if err := h.store.Write(ctx, func(tx *store.Tx) error {
					return tx.PutHostControl(ctx, "linux-1", ctl, h.clock.Now().UnixMilli())
				}); err != nil {
					t.Fatal(err)
				}
				upgradeAck(h, "linux-1", protocol.RunnerMaintenance)
				grant, err := h.app.ServerUpgradeAuthorize(ctx, restartTestBinding(c, "guard"))
				if protocol.CodeOf(err) != tc.code || grant.Allowed {
					t.Fatal(grant, err)
				}
				return
			}
			now := h.clock.Now().UnixMilli()
			ctl := upgradeCtl(h, "linux-1")
			lease := h.lease(a.RunID)
			if err := h.store.Write(ctx, func(tx *store.Tx) error {
				if err := tx.UpdateRunObserved(ctx, a.RunID, protocol.RunUnknown, now); err != nil {
					return err
				}
				if tc.name == "unknown with an active lease" {
					return nil
				}
				if err := tx.SetLease(ctx, a.LeaseID, store.LeaseActive, store.LeaseLapsed, lease.ExpiresAt, now); err != nil {
					return err
				}
				switch tc.name {
				case "lapsed in an unregistered journal":
					_, err := tx.Exec(ctx, `UPDATE runner_journal_runs SET journal_id='unregistered' WHERE run_id=?`, a.RunID)
					return err
				case "lapsed with a conflicting grant":
					ctl.Mode = protocol.RunnerMaintenance
					ctl.Authorization = &protocol.MaintenanceAuthorization{ID: "other-grant", RunIDs: []string{a.RunID}, DeadlineMillis: now + time.Minute.Milliseconds()}
					return tx.PutHostControl(ctx, "linux-1", ctl, now)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			before := h.lease(a.RunID)
			if _, err := h.app.TickUpgrade(ctx, o); protocol.CodeOf(err) != tc.code {
				t.Fatal(err)
			}
			if row := upgradeRead(h); row.View.Phase != "server_draining" || row.ServerGrantDeadline != 0 || h.lease(a.RunID) != before {
				t.Fatal("rejection changed authoritative records", row.View)
			}
		})
	}
}

func TestUpgradeChecksOriginalFixedGrantOnDrainingJournal(t *testing.T) {
	ctx := t.Context()
	h, c, rt := restartAuthorityFixture(t, "old-journal", protocol.CurrentRunnerProtocol)
	a := claimRestartWork(t, h, rt, "already bounded by a fixed grant")
	now := h.clock.Now().UnixMilli()
	grant := protocol.MaintenanceAuthorization{ID: "original-grant", RunIDs: []string{a.RunID}, DeadlineMillis: now + (8 * time.Minute).Milliseconds()}
	drain := protocol.RunnerControl{Mode: protocol.RunnerMaintenance, Revision: 1, UpgradeID: "previous", Authorization: &grant}
	drainRT := *rt
	drainRT.Mode, drainRT.AppliedControlRevision, drainRT.UpgradeID = drain.Mode, drain.Revision, drain.UpgradeID
	primary := *rt
	primary.InstanceID, primary.JournalID, primary.Generation = "new-primary", "new-journal", rt.Generation+1
	primary.Sequence = 1
	// Seed the post-handoff state, including the old, still-live authority.
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		old := store.UpgradeRow{View: protocol.UpgradeView{UpgradeID: "previous", ReleaseID: "old", CreatedAt: h.clock.Now().Add(-time.Hour), Status: "completed"}}
		if err := tx.PutUpgrade(ctx, old); err != nil {
			return err
		}
		if err := tx.PreserveDrainingRunner(ctx, "linux-1", "previous", store.HostRuntimeRow{Runtime: drainRT, LastHeartbeatAt: now}, drain); err != nil {
			return err
		}
		return tx.PutHostRuntime(ctx, "linux-1", store.HostRuntimeRow{Runtime: primary, LastHeartbeatAt: now, Online: true})
	}); err != nil {
		t.Fatal(err)
	}
	lease := h.lease(a.RunID)
	if op := startPreparedTestUpgrade(h, "restart", c); op.Error != nil {
		t.Fatal(op.Error)
	}
	if v := upgradeTick(h, ServerObservation{MainDrained: true, BinarySHA256: rt.Build.BinarySHA256}); v.Phase != "server_ready" {
		t.Fatal(v)
	}
	upgradeAck(h, "linux-1", protocol.RunnerMaintenance)
	b := restartTestBinding(c, "restart")
	if g, err := h.app.ServerUpgradeAuthorize(ctx, b); protocol.CodeOf(err) != protocol.CodeInvalidTransition || g.Allowed {
		t.Fatal("missing original grant ACK allowed restart", g, err)
	}
	drainRT.Sequence++
	drainRT.AuthorizationID, drainRT.AuthorizationDeadlineMillis = grant.ID, now+(5*time.Minute).Milliseconds()
	if _, err := h.app.RuntimeHeartbeat(ctx, runner("linux-1"), protocol.HeartbeatPayload{Runtime: &drainRT}); err != nil {
		t.Fatal(err)
	}
	g, err := h.app.ServerUpgradeAuthorize(ctx, b)
	if err != nil || !g.Allowed || g.ExpiresAt.UnixMilli() != drainRT.AuthorizationDeadlineMillis-upgradeRead(h).RestartMillis {
		t.Fatal("restart did not honor the original conservative cutoff", g, err)
	}
	if got := h.lease(a.RunID); got != lease {
		t.Fatal("fixed execution was rewritten", got, lease)
	}
}

func TestUpgradeObservesRunnerLocalCoverageAfterServerClearsGrant(t *testing.T) {
	for _, variant := range []struct {
		name    string
		scoped  bool
		short   bool
		unbound bool
	}{
		{name: "legacy expired inside restart budget", short: true},
		{name: "legacy still valid"},
		{name: "per-Run evidence expired inside restart budget", scoped: true, short: true},
		{name: "per-Run evidence still valid", scoped: true},
		{name: "an old grant unrelated to the live execution", scoped: true, unbound: true},
	} {
		t.Run(variant.name, func(t *testing.T) {
			ctx := t.Context()
			h, c, rt := restartAuthorityFixture(t, "original-journal", protocol.CurrentRunnerProtocol)
			a := claimRestartWork(t, h, rt, "live delegated execution")
			if op := startPreparedTestUpgrade(h, "next", c); op.Error != nil {
				t.Fatal(op.Error)
			}
			upgradeTick(h, ServerObservation{MainDrained: true, BinarySHA256: rt.Build.BinarySHA256})
			ack := upgradeAck(h, "linux-1", protocol.RunnerMaintenance)
			ack.Sequence++
			now := h.clock.Now()
			deadline := now.Add(3 * time.Minute).UnixMilli()
			if variant.short {
				deadline = now.Add(45 * time.Second).UnixMilli()
			}
			ack.AuthorizationID = "original-grant"
			ack.AuthorizationDeadlineMillis = deadline
			if variant.scoped {
				ack.MaintenanceCoverage = &protocol.RunnerMaintenanceCoverage{RunDeadlines: make(map[string]int64)}
				// A subsequent unrelated grant can supersede the aggregate ACK
				// while the original worker retains its own shorter deadline.
				ack.AuthorizationID = "later-grant"
				ack.AuthorizationDeadlineMillis = now.Add(8 * time.Minute).UnixMilli()
				if !variant.unbound {
					ack.MaintenanceCoverage.RunDeadlines[a.RunID] = deadline
				}
			}
			if _, err := h.app.RuntimeHeartbeat(ctx, runner("linux-1"), protocol.HeartbeatPayload{Runtime: &ack}); err != nil {
				t.Fatal(err)
			}
			if l := h.lease(a.RunID); l.MaintenanceDeadline != 0 {
				t.Fatal("fixture still has a server-side grant", l)
			}
			result, err := h.app.ServerUpgradeAuthorize(ctx, restartTestBinding(c, "next"))
			if variant.short {
				if protocol.CodeOf(err) != protocol.CodeInvalidTransition || result.Allowed {
					t.Fatal("replacement could run past the original local deadline", result, err)
				}
				return
			}
			want := deadline - upgradeRead(h).RestartMillis
			if variant.unbound {
				want = now.UnixMilli() + upgradeRead(h).GrantMillis
			}
			if err != nil || !result.Allowed || result.ExpiresAt.UnixMilli() != want {
				t.Fatal("wrong cutoff for actual execution", want, result, err)
			}
		})
	}
}

func TestUpgradeKeepsUncoveredRenewableRunLapsedAfterAuthorization(t *testing.T) {
	ctx := t.Context()
	h, c, rt := restartAuthorityFixture(t, "original-journal", protocol.MinimumRunnerProtocol)
	rt.Capabilities = append(rt.Capabilities, protocol.CapabilityRetainedReconciliation)
	original := claimRestartWork(t, h, rt, "lost original renewal")
	rt.Sequence++
	if op := h.versionedHello("linux-1", rt); op.Error != nil {
		t.Fatal(op.Error)
	}
	if op := startPreparedTestUpgrade(h, "next", c); op.Error != nil {
		t.Fatal(op.Error)
	}
	upgradeTick(h, ServerObservation{MainDrained: true, BinarySHA256: rt.Build.BinarySHA256})
	ack := upgradeAck(h, "linux-1", protocol.RunnerMaintenance)
	b := restartTestBinding(c, "next")
	if g, err := h.app.ServerUpgradeAuthorize(ctx, b); err != nil || !g.Allowed {
		t.Fatal(g, err)
	}
	ack.Sequence++
	op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "linux-1"},
		protocol.HelloPayload{Runtime: &ack, WorkspaceProtocol: 1, Held: []protocol.HeldRun{{
			RunID: original.RunID, LeaseID: original.LeaseID, Observed: protocol.RunInterrupted,
		}}}, nil), Hello{})
	var reply protocol.HelloResult
	if op.Error != nil || json.Unmarshal(op.Result, &reply) != nil || len(reply.Runs) != 1 ||
		reply.Runs[0].Verdict != protocol.VerdictRetain {
		t.Fatal("unexpected uncovered reattachment result", op, reply)
	}
	if h.lease(original.RunID).Status != store.LeaseLapsed || h.run(original.RunID).ObservedState != protocol.RunUnknown {
		t.Fatal("original Run reacquired renewal beyond the prepared coverage")
	}
}
