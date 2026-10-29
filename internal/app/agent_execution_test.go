package app

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestNamedExecutionAssignmentFreezesPinAcrossInventoryUpdateAndReplay(t *testing.T) {
	h, r, p, _ := inventorySetup(t)
	p.Agents[0].Categories = []string{"implementer"}
	p.Agents[1].Categories = []string{"reviewer"}
	_, hash, err := protocol.CanonicalAgentInventory(p)
	if err != nil {
		t.Fatal(err)
	}
	registered := h.exec(runner("linux-1"), inventoryRequest(h, r, p, hash, 1, 0, "snapshot-first"), RegisterAgentInventory{})
	if registered.Status != protocol.OpSucceeded {
		t.Fatal(registered)
	}
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentExecution)
	r.JournalVersion = 12
	for _, a := range p.Agents {
		r.AgentExecutors = append(r.AgentExecutors, protocol.AgentExecutor{Available: true, Pin: protocol.RunExecutionPin{Identity: a.Identity, AgentRevision: a.Revision, Runtime: a.Binding, InventoryRevision: 1, SnapshotID: "snapshot-first", PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: a.RequestedModel, Effort: a.RequestedEffort}, Worker: protocol.WorkerAppServer, BackendID: "git"}})
	}
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	spec := testSpec
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}
	_, task := h.createTask(owner, createTaskPayload{Title: "named", Spec: spec})
	legacy := *r
	legacy.JournalVersion = 11
	legacy.AgentExecutors = nil
	legacy.Capabilities = slices.DeleteFunc(slices.Clone(r.Capabilities), func(value string) bool { return value == protocol.CapabilityAgentExecution })
	status, err := h.app.ReadAgentInventory(t.Context(), runner("linux-1"), protocol.AgentInventoryStatusPayload{Runtime: &legacy})
	if err != nil || len(status.Executors) != 0 {
		t.Fatal("status added a field for a client without execution support", status, err)
	}
	legacyClaim := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: &legacy}, nil), Claim{})
	var unselected claimResult
	if err := json.Unmarshal(legacyClaim.Result, &unselected); err != nil || unselected.Assignment.RunID != "" {
		t.Fatal("stored capability overrode current request", legacyClaim, err)
	}
	request := h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: r}, nil)
	op := h.exec(runner("linux-1"), request, Claim{})
	var result claimResult
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	as := result.Assignment
	if as.TaskID != task.TaskID || as.Execution == nil || as.Execution.Identity.LocalID != "alpha" || as.Workspace.BackendID != "git" {
		t.Fatalf("assignment=%+v result=%s", as, op.Result)
	}
	var workerInput domain.Spec
	json.Unmarshal(as.Spec, &workerInput)
	if workerInput.AgentSelection != nil {
		t.Fatal("selection leaked into the legacy task input")
	}
	// An older-capability process cannot recover this new mode through hello.
	legacyHello := h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "linux-1"}, protocol.HelloPayload{WorkspaceProtocol: 1, Runtime: &legacy}, nil)
	if _, err := h.app.Execute(t.Context(), runner("linux-1"), legacyHello, Hello{}); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal("named recovery silently became legacy", err)
	}
	// A newer inventory changes future choices, never this run or its receipt.
	p.Agents[0].Revision++
	p.Agents[0].RequestedModel = "future-model"
	_, nextHash, err := protocol.CanonicalAgentInventory(p)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, p, nextHash, 2, 1, "snapshot-next"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	replay := h.exec(runner("linux-1"), request, Claim{})
	if string(replay.Result) != string(op.Result) {
		t.Fatal("claim retry selected current inventory")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		pin, err := store.GetRunExecution(t.Context(), tx.Q(), as.RunID)
		if err == nil && (*pin != *as.Execution || pin.SnapshotID != "snapshot-first") {
			t.Fatal("stored execution changed", pin)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestNamedWorkspaceRequiresSelectionAndLegacyMountPrecheckIsUnchanged(t *testing.T) {
	h := newHarness(t)
	cfg := testConfig()
	for i := range cfg.Hosts {
		cfg.Hosts[i].Worker = protocol.WorkerOrca
	}
	h.syncConfig(cfg)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		spec := testSpec
		spec.Workspace.Mounts = append(spec.Workspace.Mounts, domain.Mount{Repo: "other", Path: "other"})
		if err := checkSpecMounts(t.Context(), tx.Q(), spec); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
			t.Fatal("legacy multimount precheck changed", err)
		}
		// This resource can only be created by the new explicit named execution mode.
		named := store.WorkspaceRow{HostID: "linux-1", BackendID: "orca"}
		if err := checkRequiredHost(t.Context(), tx.Q(), spec, &named); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
			t.Fatal("named workspace accepted silent legacy selection", err)
		}
		legacy := named
		legacy.BackendID = ""
		if err := checkRequiredHost(t.Context(), tx.Q(), spec, &legacy); err != nil {
			t.Fatal("legacy resource newly restricted", err)
		}
		spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}
		if err := checkRequiredHost(t.Context(), tx.Q(), spec, &named); err != nil {
			t.Fatal("named input refused", err)
		}
		if err := checkSpecMounts(t.Context(), tx.Q(), spec); err != nil {
			t.Fatal("legacy kind rejected actual named backend selection", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentExecutionHelloAckIsConditional(t *testing.T) {
	h := newHarness(t)
	runtime := testRuntime("execution-negotiation")
	runtime.JournalVersion = 12
	for _, enabled := range []bool{false, true} {
		if enabled {
			runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityAgentExecution)
		}
		runtime.Sequence++
		op := h.versionedHello("linux-1", runtime)
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(op.Result, &fields); err != nil {
			t.Fatal(err)
		}
		value, exists := fields["agent_execution"]
		if exists != enabled || (enabled && string(value) != "true") {
			t.Fatal("old strict reader got a new hello field", string(op.Result))
		}
	}
}

func TestNamedWorkspaceCleanupReplyRequiresConditionalRequestSupport(t *testing.T) {
	h := newDeliverableHarness(t)
	task := h.createReportTask("retained", temporaryReportSpec())
	run, ok, _ := h.claimDeliverableRun()
	if !ok {
		t.Fatal("missing run")
	}
	h.reportDirectory(run, t.TempDir())
	h.completeReport(run)
	if !h.releaseResource(task.TaskID, run.Workspace.ID) {
		t.Fatal("resource not released")
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.BindWorkspaceBackend(t.Context(), run.Workspace.ID, "git") }); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(8 * 24 * time.Hour)
	runtime := testRuntime("cleanup-execution")
	runtime.JournalVersion = 12
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityAgentExecution)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if got := h.reserveResourceCleanup(); len(got) != 0 {
		t.Fatal("old strict client received a named backend", got)
	}
	op := h.exec(runner("linux-1"), h.env(protocol.OpWorkspaceCleanup, protocol.Target{Type: "host", ID: "linux-1"}, protocol.WorkspaceCleanupRequest{AgentExecution: true}, nil), ReserveWorkspaceCleanup{})
	var result protocol.WorkspaceCleanupResult
	if err := json.Unmarshal(op.Result, &result); err != nil || len(result.Workspaces) != 1 || result.Workspaces[0].BackendID != "git" {
		t.Fatal("negotiated cleanup lost original backend", result, err)
	}
}

func TestNamedFirstAllocationUsesCurrentOwnerTaskSpec(t *testing.T) {
	for _, previousNamed := range []bool{false, true} {
		name := "legacy_to_named"
		if previousNamed {
			name = "changed_named_spec"
		}
		t.Run(name, func(t *testing.T) {
			h, r, projection, _ := inventorySetup(t)
			projection.Agents[0].Categories = []string{"implementer"}
			projection.Agents[1].Categories = []string{"reviewer"}
			_, hash, err := protocol.CanonicalAgentInventory(projection)
			if err != nil {
				t.Fatal(err)
			}
			if op := h.exec(runner("linux-1"), inventoryRequest(h, r, projection, hash, 1, 0, "first-spec"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentExecution)
			r.JournalVersion = 12
			for _, agent := range projection.Agents {
				r.AgentExecutors = append(r.AgentExecutors, protocol.AgentExecutor{Available: true, Pin: protocol.RunExecutionPin{Identity: agent.Identity, AgentRevision: agent.Revision, Runtime: agent.Binding, InventoryRevision: 1, SnapshotID: "first-spec", PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: agent.RequestedModel, Effort: agent.RequestedEffort}, Worker: protocol.WorkerAppServer, BackendID: "git"}})
			}
			r.Sequence++
			if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			issue := createCanonicalOwnerTestIssue(t, h)
			spec := protocol.OwnerTaskSpec{Goal: "Implement the explicit owner agreement", Acceptance: []string{"Independent checks pass"}, Delivery: "commit", Workspace: protocol.OwnerTaskWorkspace{Mounts: []protocol.OwnerTaskMount{{Repo: "tend"}}}}
			selection := &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}
			if previousNamed {
				spec.AgentSelection = selection
			}
			created := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Owner task", Spec: spec, Rating: protocol.RatingP1}, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
			if created.Status != protocol.OpSucceeded {
				t.Fatal(created)
			}
			var task struct {
				TaskID string `json:"task_id"`
			}
			if err := json.Unmarshal(created.Result, &task); err != nil {
				t.Fatal(err)
			}
			old, ok := h.claimInputRun(r)
			if !ok || (old.Execution != nil) != previousNamed {
				t.Fatalf("original assignment: %+v", old)
			}
			report := protocol.WorkspaceReport{Path: t.TempDir(), Mounts: []protocol.ResolvedMount{{Path: "tend", ResolvedBaseSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ResolvedFrom: "candidate:main", Branch: "task/owner"}}}
			if previousNamed {
				report.BackendID = "git"
			}
			if op := h.runAs(old, protocol.OpRunsReportWorkspace, report, ReportWorkspace{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			// A changed selection creates a new spec, not a qualification inherited
			// from either a legacy run or an earlier named agreement.
			selection.Implementation.Model = "requested-model"
			spec.AgentSelection = selection
			issue = canonicalTaskIssue(t, h, issue.IssueID)
			current := h.task(task.TaskID)
			updated := h.exec(owner, h.env("issues.update_task", taskTarget(task.TaskID), protocol.UpdateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Spec: &spec}, rev(current.ControlRevision)), CanonicalOwnerTaskUpdate{})
			if updated.Status != protocol.OpSucceeded {
				t.Fatal(updated)
			}
			if op := h.runAs(old, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "stopped old spec after owner revision"}, Release{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			r.AgentExecutors[1].Available = false
			r.AgentExecutors[1].Reason = "test_reviewer_unavailable"
			r.Sequence++
			if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if next, ok := h.claimInputRun(r); ok {
				t.Fatalf("previous spec qualified current contract: %+v", next)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				rows, err := store.ListClaimCandidates(t.Context(), tx.Q())
				if err != nil {
					return err
				}
				found := false
				for _, row := range rows {
					if row.Action.TaskID == task.TaskID {
						found = true
						if row.NamedExecutionAssigned || row.SpecVersion != 2 {
							t.Fatalf("current spec fact: %+v", row)
						}
					}
				}
				if !found {
					t.Fatal("new spec has no pending action")
				}
				if previousNamed {
					pin, err := store.GetRunExecution(t.Context(), tx.Q(), old.RunID)
					if err != nil {
						return err
					}
					if *pin != *old.Execution {
						t.Fatal("old run pin rewritten")
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			r.AgentExecutors[1].Available = true
			r.AgentExecutors[1].Reason = ""
			r.Sequence++
			if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			next, ok := h.claimInputRun(r)
			if !ok || next.Execution == nil || next.SpecVersion != 2 {
				t.Fatalf("matching pair failed current spec: %+v", next)
			}
		})
	}
}

func TestOwnerNamedAcceptanceRejectsContradictoryLegacyProfile(t *testing.T) {
	for _, test := range []struct {
		name        string
		requirement protocol.AgentRequirement
		invalid     bool
	}{
		{"different_model", protocol.AgentRequirement{CategoryID: "reviewer", Model: "different-model"}, true},
		{"different_effort", protocol.AgentRequirement{CategoryID: "reviewer", Effort: "low"}, true},
		{"partial_match", protocol.AgentRequirement{CategoryID: "reviewer", Model: "formal-model"}, false},
		{"no_new_constraint", protocol.AgentRequirement{CategoryID: "reviewer"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			issue := createCanonicalOwnerTestIssue(t, h)
			spec := protocol.OwnerTaskSpec{Goal: "Explicit owner agreement", Acceptance: []string{"Independent checks"}, Delivery: "commit", Workspace: protocol.OwnerTaskWorkspace{Mounts: []protocol.OwnerTaskMount{{Repo: "tend"}}}, AcceptanceExecution: &protocol.ExecutionProfile{Model: "formal-model", Effort: "xhigh"}, AgentSelection: &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: test.requirement}}
			op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Owner task", Spec: spec}, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
			if test.invalid {
				expectWorkCode(t, op, protocol.CodeInvalidRequest)
				if canonicalTaskIssue(t, h, issue.IssueID).Revision != issue.Revision {
					t.Fatal("rejected task changed owner issue")
				}
				return
			}
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			var created struct {
				TaskID string `json:"task_id"`
			}
			if err := json.Unmarshal(op.Result, &created); err != nil {
				t.Fatal(err)
			}
			current := h.task(created.TaskID)
			spec.AgentSelection.Acceptance.Effort = "low"
			updated := h.exec(owner, h.env("issues.update_task", taskTarget(created.TaskID), protocol.UpdateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Spec: &spec}, rev(current.ControlRevision)), CanonicalOwnerTaskUpdate{})
			expectWorkCode(t, updated, protocol.CodeInvalidRequest)
			if h.task(created.TaskID).SpecVersion != 1 {
				t.Fatal("rejected update replaced frozen spec")
			}
		})
	}
}

func TestNamedOwnerStartOverChecksBothRolesOnReplacementHost(t *testing.T) {
	h, firstRuntime, firstProjection, _ := inventorySetup(t)
	h.withHosts("linux-1", "linux-2")
	register := func(host string, runtime *protocol.RunnerRuntime, projection protocol.AgentInventoryProjection, reviewAvailable bool) {
		t.Helper()
		projection.Agents[0].Categories = []string{"implementer"}
		projection.Agents[1].Categories = []string{"reviewer"}
		_, hash, err := protocol.CanonicalAgentInventory(projection)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(runtime.Capabilities, protocol.CapabilityAgentInventory) {
			runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityAgentInventory)
		}
		runtime.Sequence++
		if op := h.versionedHello(host, runtime); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		request := h.env(protocol.OpRunnerInventoryRegister, protocol.Target{Type: "host", ID: host}, protocol.RegisterAgentInventoryPayload{Runtime: runtime, LocalRevision: 1, SnapshotID: "accepted-" + host, PublicSHA256: hash, Projection: projection}, rev(0))
		if op := h.exec(runner(host), request, RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityAgentExecution)
		runtime.JournalVersion = 12
		for i, agent := range projection.Agents {
			observation := protocol.AgentExecutor{Available: i == 0 || reviewAvailable, Pin: protocol.RunExecutionPin{Identity: agent.Identity, AgentRevision: agent.Revision, Runtime: agent.Binding, InventoryRevision: 1, SnapshotID: "accepted-" + host, PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: agent.RequestedModel, Effort: agent.RequestedEffort}, Worker: protocol.WorkerAppServer, BackendID: "git"}}
			if !observation.Available {
				observation.Reason = "test_reviewer_unavailable"
			}
			runtime.AgentExecutors = append(runtime.AgentExecutors, observation)
		}
		runtime.Sequence++
		if op := h.versionedHello(host, runtime); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	register("linux-1", firstRuntime, firstProjection, true)
	nextRuntime := testRuntime("replacement-host")
	nextProjection, _ := inventoryProjection(t, "linux-2")
	register("linux-2", nextRuntime, nextProjection, false)
	issue := createCanonicalOwnerTestIssue(t, h)
	spec := protocol.OwnerTaskSpec{Goal: "Complete named work", Acceptance: []string{"Independent verification"}, Delivery: "commit", Workspace: protocol.OwnerTaskWorkspace{Mounts: []protocol.OwnerTaskMount{{Repo: "tend"}}}, AgentSelection: &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}}
	created := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Named owner task", Spec: spec, Rating: protocol.RatingP1}, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if created.Status != protocol.OpSucceeded {
		t.Fatal(created)
	}
	old, ok := h.claimInputRun(firstRuntime)
	if !ok || old.Execution == nil {
		t.Fatal(old)
	}
	if op := h.runAs(old, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(old, protocol.OpRunsSubmitArtifact, testArtifact(h.task(old.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.withHosts("linux-2")
	if changed := h.checkWorkspaces(); !slices.Equal(changed, []string{old.TaskID}) {
		t.Fatal(changed)
	}
	decisions := h.openDecisions(old.TaskID)
	if len(decisions) != 1 {
		t.Fatal(decisions)
	}
	if result := h.answer(decisions[0], domain.StartOverOption); result.Effect == nil || !result.Effect.Applied {
		t.Fatal(result)
	}
	after := h.task(old.TaskID)
	if after.SpecVersion != old.SpecVersion || after.ImplementGeneration != old.Generation+1 || after.WorkspaceAssigned {
		t.Fatalf("not an actual same-spec start over: %+v", after)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		pin, err := store.GetRunExecution(t.Context(), tx.Q(), old.RunID)
		if err != nil {
			return err
		}
		if *pin != *old.Execution {
			t.Fatal("start over rewrote old run pin")
		}
		rows, err := store.ListClaimCandidates(t.Context(), tx.Q())
		if err != nil {
			return err
		}
		found := false
		for _, row := range rows {
			if row.Action.TaskID == old.TaskID {
				found = true
				if row.WorkspaceHost != "" || !row.NamedExecutionAssigned {
					t.Fatalf("replacement facts: %+v", row)
				}
			}
		}
		if !found {
			t.Fatal("replacement action missing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	claim := func() (protocol.Assignment, bool) {
		return h.claimWith("linux-2", h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-2"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: nextRuntime}, nil))
	}
	if replacement, ok := claim(); ok {
		t.Fatalf("replacement host only has implementation capability: %+v", replacement)
	}
	nextRuntime.AgentExecutors[1].Available = true
	nextRuntime.AgentExecutors[1].Reason = ""
	nextRuntime.Sequence++
	if op := h.versionedHello("linux-2", nextRuntime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	replacement, ok := claim()
	if !ok || replacement.Execution == nil || replacement.Execution.Identity.ExecutionEnvironmentID != "linux-2" || replacement.Generation != old.Generation+1 || replacement.SpecVersion != old.SpecVersion {
		t.Fatalf("matching replacement pair did not claim: %+v", replacement)
	}
}
