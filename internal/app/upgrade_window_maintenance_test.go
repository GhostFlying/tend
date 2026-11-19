package app

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

// Only the earlier, already committed supervisor handoff is supplied by the
// fixture. The Run, maintenance request, retirement grant, acknowledgement and
// install phase all pass through the ordinary business entry points.
func TestUpgradeWindowMaintenance(t *testing.T) {
	for _, scenario := range []string{"work_finishes", "finishes_after_offer", "deadline", "cancel", "receipt_after_expiry", "unused_after_expiry"} {
		t.Run(scenario, func(t *testing.T) {
			h, c := selfUpgradeFixture(t)
			selfUpgradeHosts(t, h, true)
			ctx := t.Context()
			const host = "linux-1"
			target := protocol.Target{Type: "host", ID: host}
			old := upgradeAck(h, host, protocol.RunnerNormal)
			old.Build.PID = 84
			old.Sequence++
			old.Protocol, old.ProtocolMin, old.ProtocolMax = protocol.MinimumRunnerProtocol, protocol.MinimumRunnerProtocol, protocol.MinimumRunnerProtocol
			if op := h.versionedHello(host, &old); op.Error != nil {
				t.Fatal(op)
			}
			_, task := h.createTask(owner, createTaskPayload{Title: "original long run", Spec: testSpec})
			as, ok := h.claimWith(host, h.env(protocol.OpRunnerClaim, target, protocol.ClaimPayload{Worker: h.worker(host), WorkspaceProtocol: 1, Runtime: &old}, nil))
			if !ok {
				t.Fatal("old runner did not claim")
			}
			if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Error != nil {
				t.Fatal(op)
			}
			process := protocol.RecoveryIdentity{PID: 84, StartTicks: 1701, SHA256: old.Build.BinarySHA256, LaunchID: old.InstanceID}
			req := protocol.RunnerRetirement{UpgradeID: "handoff", Process: process}
			current := old
			current.InstanceID, current.JournalID, current.Generation = "compatible", "compatible", 2
			current.Protocol, current.ProtocolMin, current.ProtocolMax = protocol.CurrentRunnerProtocol, protocol.MinimumRunnerProtocol, protocol.CurrentRunnerProtocol
			current.Build.PID = 85
			old.Mode = protocol.RunnerMaintenance
			if err := h.store.Write(ctx, func(tx *store.Tx) error {
				source := store.UpgradeRow{View: protocol.UpgradeView{UpgradeID: req.UpgradeID, Status: "completed", ReleaseID: "before", CreatedAt: h.clock.Now().Add(-time.Hour)}, Hosts: []store.UpgradeHost{{HostID: host, RollingFrom: old.InstanceID, RollingProcess: process, Committed: &protocol.UpdaterReceipt{CommittedConfirmed: true}}}}
				if err := tx.PutUpgrade(ctx, source); err != nil {
					return err
				}
				row, err := store.GetHostRuntime(ctx, tx.Q(), host)
				if err != nil {
					return err
				}
				row.Runtime = old
				if err := tx.PreserveDrainingRunner(ctx, host, req.UpgradeID, row, protocol.RunnerControl{Mode: protocol.RunnerMaintenance, Revision: old.AppliedControlRevision}); err != nil {
					return err
				}
				row.Runtime = current
				return tx.PutHostRuntime(ctx, host, row)
			}); err != nil {
				t.Fatal(err)
			}
			c.Manifest.Protocol = release.Range{Min: protocol.CurrentRunnerProtocol, Max: protocol.CurrentRunnerProtocol + 1}
			c.ServerOnly, c.ServerUpdaterSupported = true, true
			c.ServerCapabilities = []string{release.ServerCapabilityRunnerJournals, release.ServerCapabilityIndependentServerUpgrade, release.ServerCapabilityWindowMaintenance}
			c.DrainMillis = 5000
			if op := startTestUpgrade(h, "ordinary", c); op.Error == nil {
				t.Fatal("ordinary upgrade passed incompatible predecessor")
			}
			c.ForceMaintenance = true
			env := h.env(c.Operation(), protocol.Target{Type: "upgrade", ID: "window"}, map[string]bool{"force_maintenance": true}, rev(0))
			system := protocol.Principal{Kind: protocol.PrincipalSystem}
			if op := h.exec(system, env, c); op.Error != nil {
				t.Fatal(op)
			}
			batch := upgradeRead(h)
			ref := protocol.LeaseRef{RunID: as.RunID, LeaseID: as.LeaseID}
			if batch.View.Phase != windowMaintenance || len(batch.View.WindowStops) != 1 || batch.View.WindowStops[0].Process != process || !slices.Equal(batch.View.WindowStops[0].Runs, []protocol.LeaseRef{ref}) {
				t.Fatal(batch.View)
			}
			if op := h.exec(system, env, c); op.Error != nil {
				t.Fatal(op)
			}
			normal := c
			normal.ForceMaintenance = false
			if _, err := h.app.CheckUpgradeStart(ctx, env, normal); err == nil {
				t.Fatal("force reused as ordinary")
			}
			old.Sequence++
			if op := h.exec(runner(host), h.env(protocol.OpRunnerHello, target, protocol.HelloPayload{Runtime: &old, Worker: h.worker(host), WorkspaceProtocol: 1, Held: []protocol.HeldRun{{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunRunning}}}, nil), Hello{}); op.Error != nil {
				t.Fatal("pinned old runner cannot reconnect", op)
			}
			foreign := old
			foreign.InstanceID = "different-old"
			if op := h.versionedHello(host, &foreign); op.Error == nil {
				t.Fatal("foreign incompatible runner admitted")
			}
			old.Sequence++
			hb, err := h.app.RuntimeHeartbeat(ctx, runner(host), protocol.HeartbeatPayload{Runtime: &old, Runs: []protocol.LeaseRef{ref}})
			if err != nil || len(hb.Runs) != 1 || !hb.Runs[0].Renewed {
				t.Fatal(hb, err)
			}
			retire := func(r protocol.RunnerRetirement) protocol.RunnerRetirementResult {
				t.Helper()
				op := h.exec(runner(host), h.env("runner.rolling_retire", target, r, nil), RetireRollingRunner{})
				var result protocol.RunnerRetirementResult
				if op.Error != nil || json.Unmarshal(op.Result, &result) != nil {
					t.Fatal(op)
				}
				return result
			}
			control := func(act string) error {
				row := upgradeRead(h)
				op, err := h.app.Execute(ctx, owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: row.View.UpgradeID}, protocol.UpgradeControlPayload{Action: act}, rev(row.View.Revision)), ControlUpgrade{})
				if err != nil {
					return err
				}
				if op.Error != nil {
					return op.Error
				}
				return nil
			}
			if r := retire(req); r.Allowed || r.WindowID != batch.View.UpgradeID {
				t.Fatal("busy runner must receive an immediate bounded stop offer", r)
			}
			if v := upgradeTick(h, ServerObservation{MainDrained: true}); v.Phase != windowMaintenance {
				t.Fatal("premature install", v)
			}
			if scenario == "cancel" {
				if op := control("cancel"); op != nil {
					t.Fatal(op)
				}
				h.clock.Advance(6 * time.Second)
				if r := retire(req); r.Allowed {
					t.Fatal("cancelled authority", r)
				}
				return
			}
			natural := scenario == "work_finishes" || scenario == "finishes_after_offer"
			if scenario == "finishes_after_offer" {
				offer := retire(req)
				if offer.Allowed || offer.WindowID == "" {
					t.Fatal(offer)
				}
				req.WindowID = offer.WindowID
			}
			if natural {
				if op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task.TaskID).WorkspaceName), SubmitArtifact{}); op.Error != nil {
					t.Fatal(op)
				}
				old.Sequence++
				old.Mode = protocol.RunnerReady
				if _, err := h.app.RuntimeHeartbeat(ctx, runner(host), protocol.HeartbeatPayload{Runtime: &old}); err != nil {
					t.Fatal(err)
				}
			}
			wrong := req
			wrong.Process.StartTicks++
			if result := retire(wrong); result.Allowed {
				t.Fatal("different process authorized")
			}
			if req.WindowID == "" {
				offer := retire(req)
				if offer.Allowed || offer.WindowID != batch.View.UpgradeID {
					t.Fatal("historical supervisor received an unbounded grant", offer)
				}
				req.WindowID = offer.WindowID
			}
			grant := retire(req)
			if grant.WindowID != batch.View.UpgradeID {
				t.Fatal("emergency stop lost its deadline", grant)
			}
			if !grant.Allowed || grant.AuthorizationID == "" || retire(req).AuthorizationID != grant.AuthorizationID {
				t.Fatal("missing original permission", grant)
			}
			if v := upgradeTick(h, ServerObservation{MainDrained: true}); v.Phase != windowMaintenance || v.WindowStops[0].State != "stop_authorized" {
				t.Fatal("grant treated as exit", v)
			}
			if op := control("cancel"); op == nil {
				t.Fatal("dropped unresolved stop")
			}
			if scenario == "receipt_after_expiry" || scenario == "unused_after_expiry" {
				h.clock.Advance(21 * time.Minute)
				if v := upgradeTick(h, ServerObservation{}); v.Status != "attention" {
					t.Fatal(v)
				}
				if retire(req).Allowed {
					t.Fatal("expired stop was reissued")
				}
			}
			req.AuthorizationID = grant.AuthorizationID
			if scenario == "unused_after_expiry" {
				req.Unused = true
				for range 2 {
					if res := retire(req); res.Allowed || res.Confirmed || !res.Unused {
						t.Fatal("unused grant became an exit receipt", res)
					}
				}
				if v := upgradeRead(h).View; v.WindowStops[0].State != "not_stopped" || h.lease(as.RunID).Status != store.LeaseActive {
					t.Fatal(v)
				}
				if err := control("cancel"); err != nil {
					t.Fatal(err)
				}
				return
			}
			for attempt := 1; attempt <= 2; attempt++ {
				if !retire(req).Confirmed {
					t.Fatalf("original stop receipt was unconfirmed on attempt %d", attempt)
				}
			}
			stopped := upgradeRead(h).View.WindowStops[0]
			if stopped.State != "stopped" {
				t.Fatal(stopped)
			}
			if !natural {
				if !slices.Equal(stopped.UnknownRuns, []string{as.RunID}) || h.run(as.RunID).ObservedState != protocol.RunUnknown || h.lease(as.RunID).Status != store.LeaseLapsed || h.lease(as.RunID).LeaseID != as.LeaseID {
					t.Fatal("original work lost", stopped, h.run(as.RunID), h.lease(as.RunID))
				}
				if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
					ok, err := store.WindowRunStopped(ctx, tx.Q(), host, as.RunID)
					if !ok {
						t.Error("original retirement proof lost")
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			} else if len(stopped.UnknownRuns) > 0 || h.run(as.RunID).ObservedState != protocol.RunFinished {
				t.Fatal("completed result overwritten", stopped, h.run(as.RunID))
			}
			if scenario == "deadline" {
				h.createTask(owner, createTaskPayload{Title: "available physical slot", Spec: testSpec})
				if next, ok := h.claimWith(host, h.env(protocol.OpRunnerClaim, target, protocol.ClaimPayload{Worker: h.worker(host), WorkspaceProtocol: 1, Runtime: &current}, nil)); !ok || next.RunID == as.RunID {
					t.Fatal("compatible runner could not use the released physical slot", next)
				}
			}
			v := upgradeTick(h, ServerObservation{})
			if scenario == "receipt_after_expiry" {
				if v.Status != "attention" {
					t.Fatal("expired batch resumed", v)
				}
			} else if v.Phase != "preparing" || upgradeTick(h, ServerObservation{}).Phase != "server_updater" {
				t.Fatal("receipt did not release installation", v)
			}
		})
	}
}

func TestManagerParkingAfterConfirmedWindowStop(t *testing.T) {
	h, _, assignment, _ := parkUpgradeFixture(t)
	ctx := t.Context()
	h.clock.Advance(2 * time.Minute)
	if _, err := h.app.ExpireLeases(ctx); err != nil {
		t.Fatal(err)
	}
	if h.run(assignment.RunID).ObservedState != protocol.RunUnknown {
		t.Fatal("expected original lapsed manager")
	}
	// Supply only the earlier physical exit receipt. The subsequent batch must
	// still park an unknown manager whose lease was not covered by that receipt.
	now := h.clock.Now()
	batch := store.UpgradeRow{View: protocol.UpgradeView{UpgradeID: "park", ReleaseID: "next", CreatedAt: now}, Hosts: []store.UpgradeHost{{HostID: "linux-1"}}}
	for _, proved := range []bool{false, true} {
		err := h.store.Write(ctx, func(tx *store.Tx) error {
			if proved {
				if err := seedWindowExit(ctx, tx, now, assignment); err != nil {
					return err
				}
			}
			batch.ManagerParks = nil
			return captureUpgradeManagers(ctx, &Exec{Now: now, IDs: h.ids}, tx, &batch)
		})
		if err != nil {
			t.Fatal(err)
		}
		if (len(batch.ManagerParks) == 0) != proved {
			t.Fatal(proved, batch.ManagerParks)
		}
	}
}

// The completed handoff and physical receipt precede the business operation under test.
func seedWindowExit(ctx context.Context, tx *store.Tx, now time.Time, assignment protocol.Assignment) error {
	source := store.UpgradeRow{View: protocol.UpgradeView{UpgradeID: "handoff", ReleaseID: "old", CreatedAt: now.Add(-time.Hour), Status: "completed"}}
	if err := tx.PutUpgrade(ctx, source); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO runner_retirements(host_id,instance_id,upgrade_id,journal_id,authorization_id,process_json,confirmed_at) VALUES('linux-1','old','handoff','','original-stop','{}',?)`, now.UnixMilli()); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO runner_window_released_leases VALUES(?,?,'original-stop')`, assignment.RunID, assignment.LeaseID); err != nil {
		return err
	}
	return nil
}

func TestEmergencyWindowStopsSoleAcceptingRunner(t *testing.T) {
	for _, busy := range []bool{false, true} {
		name := "idle"
		if busy {
			name = "running"
		}
		t.Run(name, func(t *testing.T) {
			h, c := selfUpgradeFixture(t)
			selfUpgradeHosts(t, h, true)
			ctx := t.Context()
			const host = "linux-1"
			rt := upgradeAck(h, host, protocol.RunnerNormal)
			rt.Build.PID = 84
			rt.Sequence++
			rt.Protocol, rt.ProtocolMin, rt.ProtocolMax = protocol.MinimumRunnerProtocol, protocol.MinimumRunnerProtocol, protocol.MinimumRunnerProtocol
			if op := h.versionedHello(host, &rt); op.Error != nil {
				t.Fatal(op.Error)
			}
			var ref protocol.Assignment
			if busy {
				_, _ = h.createTask(owner, createTaskPayload{Title: "busy accepting runner", Spec: testSpec})
				var ok bool
				ref, ok = h.claimWith(host, h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: host}, protocol.ClaimPayload{Worker: h.worker(host), WorkspaceProtocol: 1, Runtime: &rt}, nil))
				if !ok {
					t.Fatal("initial claim")
				}
				if op := h.runAs(ref, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Error != nil {
					t.Fatal(op.Error)
				}
			}
			c.ServerOnly, c.ServerUpdaterSupported = true, true
			c.Manifest.Protocol = release.Range{Min: protocol.CurrentRunnerProtocol, Max: protocol.CurrentRunnerProtocol + 1}
			c.ServerCapabilities = []string{release.ServerCapabilityRunnerJournals, release.ServerCapabilityIndependentServerUpgrade, release.ServerCapabilityWindowMaintenance}
			if op := startTestUpgrade(h, "ordinary", c); op.Error == nil {
				t.Fatal("ordinary upgrade passed sole incompatible instance")
			}
			c.ForceMaintenance = true
			if op := startTestUpgrade(h, "emergency", c); op.Error != nil {
				t.Fatal(op.Error)
			}
			batch := upgradeRead(h)
			if len(batch.View.WindowStops) != 1 || !batch.View.WindowStops[0].Primary || upgradeCtl(h, host).Mode != protocol.RunnerMaintenance {
				t.Fatal(batch.View)
			}
			process := protocol.RecoveryIdentity{PID: 84, StartTicks: 100, SHA256: rt.Build.BinarySHA256, LaunchID: rt.InstanceID}
			id, err := h.app.PrimaryWindowTarget(ctx, runner(host), process)
			if err != nil || id != "emergency" {
				t.Fatal(id, err)
			}
			if id, err := h.app.PrimaryWindowTarget(ctx, runner("linux-2"), process); err != nil || id != "" {
				t.Fatal("foreign host discovery", id, err)
			}
			req := protocol.RunnerRetirement{WindowID: id, UpgradeID: id, Process: process}
			retire := func(req protocol.RunnerRetirement) protocol.RunnerRetirementResult {
				t.Helper()
				op := h.exec(runner(host), h.env("runner.rolling_retire", protocol.Target{Type: "host", ID: host}, req, nil), RetireRollingRunner{})
				var out protocol.RunnerRetirementResult
				if op.Error != nil || json.Unmarshal(op.Result, &out) != nil {
					t.Fatal(op)
				}
				return out
			}
			grant := retire(req)
			if !grant.Allowed || grant.RemainingMillis <= 0 {
				t.Fatal(grant)
			}
			if v := upgradeTick(h, ServerObservation{MainDrained: true}); v.Phase != windowMaintenance {
				t.Fatal(v)
			}
			req.AuthorizationID = grant.AuthorizationID
			for range 2 {
				if !retire(req).Confirmed {
					t.Fatal("missing confirmation")
				}
			}
			if busy && (h.run(ref.RunID).ObservedState != protocol.RunUnknown || h.lease(ref.RunID).Status != store.LeaseLapsed) {
				t.Fatal("original work not reconciled")
			}
			if op := h.versionedHello(host, &rt); op.Error == nil {
				t.Fatal("retired instance re-registered")
			}
			if ctl := upgradeCtl(h, host); ctl.Mode != protocol.RunnerMaintenance || ctl.UpgradeID != "" {
				t.Fatal(ctl)
			}
			if v := upgradeTick(h, ServerObservation{}); v.Phase != "preparing" {
				t.Fatal(v)
			}
		})
	}
}

func TestWindowFleetEvidencePreservesPhysicalHostOrder(t *testing.T) {
	h, _ := selfUpgradeFixture(t)
	now := h.clock.Now()
	r := store.UpgradeRow{View: protocol.UpgradeView{UpgradeID: "fleet", ReleaseID: "target", Status: "completed", Phase: "completed", CreatedAt: now, UpdatedAt: now, Hosts: []protocol.UpgradeHostView{{HostID: "stopped", Status: "skipped", Phase: "offline_maintenance"}, {HostID: "live", Status: "skipped"}}}, Manifest: release.Manifest{ReleaseID: "target"}, ManifestSHA256: "manifest", ServerHash: "server", Hosts: []store.UpgradeHost{{HostID: "stopped", PrimaryOffline: true}, {HostID: "live", PreviousHash: "same", TargetHash: "same"}}}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), r) }); err != nil {
		t.Fatal(err)
	}
	err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		proof, err := store.UpgradeInstallationEvidence(t.Context(), tx.Q(), "fleet", now.UnixMilli())
		if err == nil && (len(proof.Hosts) != 1 || proof.Hosts[0].HostID != "live") {
			t.Fatal(proof)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReattachedLeaseRequiresItsOwnWindowExit(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	const host = "linux-1"
	rt := testRuntime("original")
	if op := h.versionedHello(host, rt); op.Error != nil {
		t.Fatal(op.Error)
	}
	h.createTask(owner, createTaskPayload{Title: "long run", Spec: testSpec})
	target := protocol.Target{Type: "host", ID: host}
	as, ok := h.claimWith(host, h.env(protocol.OpRunnerClaim, target, protocol.ClaimPayload{Worker: h.worker(host), WorkspaceProtocol: 1, Runtime: rt}, nil))
	if !ok {
		t.Fatal("missing claim")
	}
	if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Error != nil {
		t.Fatal(op.Error)
	}
	h.clock.Advance(2 * time.Minute)
	if _, err := h.app.ExpireLeases(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(ctx, func(tx *store.Tx) error { return seedWindowExit(ctx, tx, h.clock.Now(), as) }); err != nil {
		t.Fatal(err)
	}
	replacement := *rt
	replacement.InstanceID = "replacement"
	replacement.Generation++
	replacement.Sequence = 1
	hello := protocol.HelloPayload{Worker: h.worker(host), WorkspaceProtocol: 1, Runtime: &replacement, Held: []protocol.HeldRun{{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunRunning}}}
	if op := h.exec(runner(host), h.env(protocol.OpRunnerHello, target, hello, nil), Hello{}); op.Error != nil {
		t.Fatal(op.Error)
	}
	if h.run(as.RunID).ObservedState != protocol.RunRunning {
		t.Fatal("replacement did not reattach")
	}
	h.clock.Advance(2 * time.Minute)
	if _, err := h.app.ExpireLeases(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		open, occupied, err := store.HostSlotCounts(ctx, tx.Q(), host)
		if err == nil && (open != 1 || occupied != 1) {
			t.Fatal("previous writer released replacement occupancy", open, occupied)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
