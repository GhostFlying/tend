package app

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func capabilitySetup(t *testing.T) (*harness, *protocol.RunnerRuntime, protocol.RunExecutionPin) {
	t.Helper()
	h, r, p, _ := inventorySetup(t)
	r.JournalVersion = 17
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentExecution, protocol.CapabilityExecutorEvidence, protocol.CapabilityExecutorDeclarations)
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	p.CapabilityVersion = 1
	p.Agents[0].Categories = []string{"implementer"}
	p.Agents[0].Capabilities = []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 1}}
	p.Agents[1].Categories = []string{"reviewer"}
	_, hash, err := protocol.CanonicalAgentInventory(p)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, p, hash, 1, 0, "capability-snapshot"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	for _, a := range p.Agents {
		r.AgentExecutors = append(r.AgentExecutors, protocol.AgentExecutor{Available: true, Pin: protocol.RunExecutionPin{Identity: a.Identity, AgentRevision: a.Revision, Runtime: a.Binding, InventoryRevision: 1, SnapshotID: "capability-snapshot", PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: a.RequestedModel, Effort: a.RequestedEffort}, Worker: protocol.WorkerAppServer, BackendID: "git"}})
	}
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return h, r, r.AgentExecutors[0].Pin
}

func capabilityEvidence(h *harness, r *protocol.RunnerRuntime, p protocol.RunExecutionPin, sequence int64) protocol.ExecutorCapabilityEvidence {
	digest := "sha256:" + strings.Repeat("b", 64)
	return protocol.ExecutorCapabilityEvidence{Capability: protocol.CapabilityReference{CapabilityID: "image_gen", DefinitionRevision: 1}, Pin: p, InstanceID: r.InstanceID, Generation: r.Generation, Sequence: sequence, Kind: "tool_inventory", Scope: "profile", Complete: true, State: "confirmed", Reason: "tool_exposed", Source: &protocol.CapabilityToolSource{Kind: "native", ID: "fixture-registry", Version: "v1", ToolName: "image_gen__imagegen", SchemaSHA256: digest, ConfigurationSHA256: digest, ExecutableSHA256: digest}, ObservedAtMillis: h.clock.Now().UnixMilli(), ExpiresAtMillis: h.clock.Now().Add(5 * time.Minute).UnixMilli()}
}

func capabilityTask(t *testing.T, h *harness) string {
	t.Helper()
	s := testSpec
	s.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer", RequiredCapabilities: []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 1}}}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}
	_, task := h.createTask(owner, createTaskPayload{Title: "capability constrained", Spec: s})
	return task.TaskID
}

func TestCapabilityDefinitionCASHistoryIdempotencyAndCursor(t *testing.T) {
	h := newHarness(t)
	p := protocol.PutCapabilityPayload{Title: "Render", State: "active", ContractVersion: 1, VerificationPolicy: "declaration_v1"}
	first := h.env("capabilities.put", protocol.Target{Type: "capability", ID: "render"}, p, rev(0))
	if op := h.exec(owner, first, PutCapability{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	a, b := h.env("capabilities.put", first.Target, p, rev(1)), h.env("capabilities.put", first.Target, p, rev(1))
	results := make(chan protocol.Operation, 2)
	for _, env := range []*protocol.Envelope{a, b} {
		go func() {
			op, err := h.app.Execute(t.Context(), owner, env, PutCapability{})
			if err != nil {
				results <- protocol.Operation{Error: &protocol.Error{Code: protocol.CodeInternal}}
				return
			}
			results <- op
		}()
	}
	winners, stale := 0, 0
	for range 2 {
		op := <-results
		if op.Status == protocol.OpSucceeded {
			winners++
		} else if op.Error != nil && op.Error.Code == protocol.CodeStaleRevision {
			stale++
		}
	}
	if winners != 1 || stale != 1 {
		t.Fatal(winners, stale)
	}
	page, err := h.app.CapabilityDefinitions(t.Context(), "render", "all", "", "", 1)
	if err != nil || len(page.Items) != 1 || page.Next == "" {
		t.Fatal(page, err)
	}
	p.State = "retired"
	if op := h.exec(owner, h.env("capabilities.put", first.Target, p, rev(2)), PutCapability{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	page2, err := h.app.CapabilityDefinitions(t.Context(), "render", "all", "", page.Next, 1)
	if err != nil || len(page2.Items) != 1 || page2.Items[0].Revision != 2 || page2.Next != "" {
		t.Fatal("watermark moved", page2, err)
	}
	if _, err := h.app.CapabilityDefinitions(t.Context(), "render", "active", "", page.Next, 1); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal("cursor changed filters", err)
	}
	var d protocol.CapabilityDefinition
	op := h.exec(owner, first, PutCapability{})
	if err := json.Unmarshal(op.Result, &d); err != nil || d.Revision != 1 || d.State != "active" {
		t.Fatal(d, err)
	}
	first.Payload, _ = json.Marshal(p)
	if op, err := h.app.Execute(t.Context(), owner, first, PutCapability{}); protocol.CodeOf(err) != protocol.CodeConflictPayload && (op.Error == nil || op.Error.Code != protocol.CodeConflictPayload) {
		t.Fatal("changed request accepted", op, err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return checkCapabilityReferences(t.Context(), tx.Q(), []protocol.CapabilityReference{{CapabilityID: "render", DefinitionRevision: 1}})
	}); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal("retired definition accepted for new spec", err)
	}
}

func TestCapabilityClaimUsesDeclarationAndFreezesBindingAtomically(t *testing.T) {
	h, r, pin := capabilitySetup(t)
	taskID := capabilityTask(t, h)
	claim := func() *protocol.Envelope {
		return h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: r}, nil)
	}
	var result claimResult
	explanation, err := h.app.TaskCapabilityExplanation(t.Context(), taskID, 0, protocol.RoleImplement)
	if err != nil || explanation.State != "eligible" || len(explanation.Required) != 1 {
		t.Fatal(explanation, err)
	}
	e := capabilityEvidence(h, r, pin, 2)
	report := h.env(protocol.OpRunnerCapabilityEvidence, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ReportCapabilityEvidencePayload{Runtime: r, Evidence: e}, rev(1))
	if op := h.exec(runner("linux-1"), report, ReportCapabilityEvidence{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// A lower sequence can be retained as history but cannot revoke or refresh
	// the current observation. Execution evidence also cannot grant inventory.
	late := e
	late.Sequence = 1
	late.State = "unverified"
	late.Reason = "probe_timeout"
	if op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerCapabilityEvidence, report.Target, protocol.ReportCapabilityEvidencePayload{Runtime: r, Evidence: late}, rev(1)), ReportCapabilityEvidence{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	explanation, err = h.app.TaskCapabilityExplanation(t.Context(), taskID, 0, protocol.RoleImplement)
	if err != nil || explanation.State != "eligible" {
		t.Fatal("claim/explain disagree", explanation, err)
	}
	// Fail after the run and pin were inserted: no partial claim may remain.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER fail_capability_binding BEFORE INSERT ON run_capability_bindings BEGIN SELECT RAISE(ABORT,'injected failure'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Execute(t.Context(), runner("linux-1"), claim(), Claim{}); err == nil {
		t.Fatal("injected transaction failure succeeded")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		runs, err := store.ListRunsByTask(t.Context(), tx.Q(), taskID)
		if len(runs) != 0 {
			t.Fatal("partial run survived", runs)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DROP TRIGGER fail_capability_binding`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	request := claim()
	op := h.exec(runner("linux-1"), request, Claim{})
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	as := result.Assignment
	if as.TaskID != taskID || as.CapabilityBinding == nil || as.Execution == nil || *as.Execution != pin || len(as.CapabilityBinding.EvidenceIDs) != 0 || len(as.CapabilityBinding.Evidence) != 0 {
		t.Fatalf("binding missing: %+v", as)
	}
	if replay := h.exec(runner("linux-1"), request, Claim{}); string(replay.Result) != string(op.Result) {
		t.Fatal("claim replay changed frozen evidence")
	}
	var worker domain.Spec
	if err := json.Unmarshal(as.Spec, &worker); err != nil || !strings.Contains(worker.Scope, "image_gen@1") || worker.AgentSelection != nil {
		t.Fatal("role constraints lost or selection leaked", worker, err)
	}
	legacy := *r
	legacy.Capabilities = append([]string(nil), r.Capabilities...)
	for i, v := range legacy.Capabilities {
		if v == protocol.CapabilityExecutorDeclarations {
			legacy.Capabilities = append(legacy.Capabilities[:i], legacy.Capabilities[i+1:]...)
			break
		}
	}
	if _, err := h.app.Execute(t.Context(), runner("linux-1"), h.env(protocol.OpRunnerHello, request.Target, protocol.HelloPayload{WorkspaceProtocol: 1, Runtime: &legacy}, nil), Hello{}); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal("old peer recovered constrained run", err)
	}
}

func TestCapabilityDeclarationIgnoresEvidenceExpiryAndIncarnation(t *testing.T) {
	h, r, pin := capabilitySetup(t)
	task := capabilityTask(t, h)
	e := capabilityEvidence(h, r, pin, 1)
	report := h.env(protocol.OpRunnerCapabilityEvidence, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ReportCapabilityEvidencePayload{Runtime: r, Evidence: e}, rev(1))
	if _, err := h.app.Execute(t.Context(), owner, report, ReportCapabilityEvidence{}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("owner fabricated evidence", err)
	}
	if op := h.exec(runner("linux-1"), report, ReportCapabilityEvidence{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	page, err := h.app.CapabilityAgents(t.Context(), protocol.AgentCapabilityQuery{Environment: "linux-1", Agent: "alpha"})
	if err != nil || len(page.Items) != 1 || page.Items[0].Current.State != "eligible" {
		t.Fatal(page, err)
	}
	h.clock.Advance(5 * time.Minute)
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	explain, err := h.app.TaskCapabilityExplanation(t.Context(), task, 0, protocol.RoleImplement)
	if err != nil || explain.State != "eligible" {
		t.Fatal("tool TTL blocked declaration", explain, err)
	}
	if op := h.exec(runner("linux-1"), report, ReportCapabilityEvidence{}); op.Status != protocol.OpSucceeded {
		t.Fatal("expired evidence history request lost", op)
	}
	r.InstanceID = "replacement"
	r.Generation++
	r.Sequence = 1
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	page, err = h.app.CapabilityAgents(t.Context(), protocol.AgentCapabilityQuery{Environment: "linux-1", Agent: "alpha"})
	if err != nil || page.Items[0].Current.State != "eligible" || len(page.Items[0].Evidence) != 1 {
		t.Fatal("incarnation lost declaration or erased history", page, err)
	}
}

func TestCapabilityWaitNoticeIsBoundedAndDoesNotProbe(t *testing.T) {
	h, _, _ := capabilitySetup(t)
	capabilityTask(t, h)
	h.clock.Advance(11 * time.Minute)
	if err := h.app.SweepCapabilityWaits(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SweepCapabilityWaits(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var n int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM capability_wait_notices`).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatal("notice duplicated", n)
		}
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM executor_capability_evidence`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("wait triggered a probe", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilityRequiredInputFreezeFilteringAndPagination(t *testing.T) {
	h, r, pin := capabilitySetup(t)
	taskID := capabilityTask(t, h)
	spec := h.storedSpec(taskID, 1)
	if len(spec.AgentSelection.Implementation.RequiredCapabilities) != 1 || len(spec.AgentSelection.Acceptance.RequiredCapabilities) != 0 {
		t.Fatal("two roles were conflated", spec.AgentSelection)
	}
	for _, refs := range [][]protocol.CapabilityReference{
		{{CapabilityID: "missing", DefinitionRevision: 1}},
		{{CapabilityID: "image_gen", DefinitionRevision: 99}},
		{{CapabilityID: "image_gen", DefinitionRevision: 1}, {CapabilityID: "image_gen", DefinitionRevision: 1}},
	} {
		bad := spec
		selection := spec.AgentSelection.Clone()
		selection.Implementation.RequiredCapabilities = refs
		bad.AgentSelection = &selection
		if op, _ := h.createTask(owner, createTaskPayload{Title: "invalid required", Spec: bad}); op.Status == protocol.OpSucceeded {
			t.Fatal("invalid required accepted", refs)
		}
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		rows, err := store.ListTaskPage(t.Context(), tx.Q(), store.TaskFilter{Capability: "image_gen", CapabilityRole: "implement"}, 0, "", 10)
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].TaskID != taskID {
			t.Fatal(rows)
		}
		rows, err = store.ListTaskPage(t.Context(), tx.Q(), store.TaskFilter{Capability: "image_gen", CapabilityRole: "accept"}, 0, "", 10)
		if err != nil {
			return err
		}
		if len(rows) != 0 {
			t.Fatal("acceptance inherited image generation", rows)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	page, err := h.app.CapabilityAgents(t.Context(), protocol.AgentCapabilityQuery{Limit: 1})
	if err != nil || page.Next == "" || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	page2, err := h.app.CapabilityAgents(t.Context(), protocol.AgentCapabilityQuery{Limit: 1, After: page.Next})
	if err != nil || len(page2.Items) != 1 || page2.Items[0].Identity == page.Items[0].Identity {
		t.Fatal(page2, err)
	}
	if _, err := h.app.CapabilityAgents(t.Context(), protocol.AgentCapabilityQuery{Limit: 1, After: page.Next, State: "pending"}); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal("agent cursor changed filter", err)
	}
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, err := h.app.CapabilityAgents(t.Context(), protocol.AgentCapabilityQuery{Limit: 1, After: page.Next}); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal("changed source silently paginated", err)
	}
	e := capabilityEvidence(h, r, pin, 1)
	e.Scope = "session"
	e.SessionID = "native-session"
	if op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerCapabilityEvidence, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ReportCapabilityEvidencePayload{Runtime: r, Evidence: e}, rev(1)), ReportCapabilityEvidence{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	explanation, err := h.app.TaskCapabilityExplanationPage(t.Context(), taskID, 1, protocol.RoleImplement, protocol.AgentCapabilityQuery{Limit: 1})
	if err != nil || explanation.State != "eligible" || len(explanation.Candidates) != 1 || explanation.Next == "" {
		t.Fatal("session evidence gated declaration or pagination absent", explanation, err)
	}
	page3, err := h.app.TaskCapabilityExplanationPage(t.Context(), taskID, 1, protocol.RoleImplement, protocol.AgentCapabilityQuery{Limit: 1, After: explanation.Next})
	if err != nil || len(page3.Candidates) != 1 || page3.Candidates[0].Identity == explanation.Candidates[0].Identity {
		t.Fatal(page3, err)
	}
}

func TestCapabilityReconnectRetainsOriginalPinAndDeclarationWithoutRevalidation(t *testing.T) {
	h, r, pin := capabilitySetup(t)
	taskID := capabilityTask(t, h)
	e := capabilityEvidence(h, r, pin, 1)
	target := protocol.Target{Type: "host", ID: "linux-1"}
	if op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerCapabilityEvidence, target, protocol.ReportCapabilityEvidencePayload{Runtime: r, Evidence: e}, rev(1)), ReportCapabilityEvidence{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, target, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: r}, nil), Claim{})
	var claimed claimResult
	if err := json.Unmarshal(op.Result, &claimed); err != nil || claimed.Assignment.CapabilityBinding == nil {
		t.Fatal(op, err)
	}
	selected, _ := json.Marshal(claimed.Assignment.CapabilityBinding.Evidence)
	var inv store.AgentInventoryRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		inv, err = store.GetAgentInventory(t.Context(), tx.Q(), "linux-1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	inv.Projection.Agents[1].Revision++
	_, hash, err := protocol.CanonicalAgentInventory(inv.Projection)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, inv.Projection, hash, 2, 1, "new-unrelated-snapshot"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	for i := range r.AgentExecutors {
		r.AgentExecutors[i].Pin.SnapshotID = "new-unrelated-snapshot"
		r.AgentExecutors[i].Pin.PublicSHA256 = hash
		r.AgentExecutors[i].Pin.InventoryRevision = 2
		if i == 1 {
			r.AgentExecutors[i].Pin.AgentRevision++
		}
	}
	r.AgentContinuations = []protocol.AgentExecutor{{Pin: pin, Available: true}}
	r.ContinuationBackend = "git"
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentContinuation)
	h.clock.Advance(6 * time.Minute)
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	fresh := capabilityEvidence(h, r, pin, 2)
	report := h.env(protocol.OpRunnerCapabilityEvidence, target, protocol.ReportCapabilityEvidencePayload{Runtime: r, Evidence: fresh}, rev(2))
	if op := h.exec(runner("linux-1"), report, ReportCapabilityEvidence{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		original, err := store.GetRunCapabilityBinding(t.Context(), tx.Q(), claimed.Assignment.RunID)
		if err != nil {
			return err
		}
		body, _ := json.Marshal(original.Evidence)
		if string(body) != string(selected) || original.Pin != pin {
			t.Fatal("revalidation rewrote original selection")
		}
		run, err := store.GetRun(t.Context(), tx.Q(), claimed.Assignment.RunID)
		if err != nil {
			return err
		}
		lease, err := store.GetLeaseByRun(t.Context(), tx.Q(), claimed.Assignment.RunID)
		if err != nil {
			return err
		}
		as, err := buildAssignment(t.Context(), tx.Q(), run, lease, h.app.leaseTTL, h.clock.Now().UnixMilli())
		if err != nil {
			return err
		}
		if as.TaskID != taskID || as.Execution == nil || *as.Execution != pin || as.CapabilityBinding.Revalidation != nil || as.CapabilityBinding.CurrentMatch.State != "eligible" {
			t.Fatal("same-entry recovery missing", as.CapabilityBinding)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilityExplanationKeepsFrozenIdentityRuntimeAndBackend(t *testing.T) {
	for _, tc := range []string{"agent", "runtime"} {
		t.Run(tc, func(t *testing.T) {
			h, _, pin := capabilitySetup(t)
			s := testSpec
			req := protocol.AgentRequirement{CategoryID: "implementer", RequiredCapabilities: []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 1}}}
			if tc == "agent" {
				req.AgentID = "linux-1/unavailable"
			}
			if tc == "runtime" {
				req.RuntimeID = "unavailable"
			}
			s.AgentSelection = &protocol.TaskAgentSelection{Implementation: req, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}
			_, task := h.createTask(owner, createTaskPayload{Title: "frozen identity", Spec: s})

			explanation, err := h.app.TaskCapabilityExplanation(t.Context(), task.TaskID, 0, protocol.RoleImplement)
			if err != nil || explanation.State == "eligible" {
				t.Fatal(explanation, err)
			}
			found := false
			for _, v := range explanation.Candidates {
				if v.Identity == pin.Identity {
					found = true
					if v.SchedulingReason == nil {
						t.Fatal("explanation replaced frozen constraint", v)
					}
				}
			}
			if !found {
				t.Fatal("missing available declaration candidate", explanation)
			}
		})
	}
}

func TestCapabilityLegacyBindingRemainsRecoverableByEvidenceOnlyPeer(t *testing.T) {
	h, r, _ := capabilitySetup(t)
	spec := testSpec
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}
	_, task := h.createTask(owner, createTaskPayload{Title: "legacy run", Spec: spec})
	target := protocol.Target{Type: "host", ID: "linux-1"}
	op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, target, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: r}, nil), Claim{})
	var claimed claimResult
	if err := json.Unmarshal(op.Result, &claimed); err != nil || claimed.Assignment.TaskID != task.TaskID || claimed.Assignment.Execution == nil {
		t.Fatal(op, err)
	}
	pin := *claimed.Assignment.Execution
	refs := []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 1}}
	legacy := protocol.RunCapabilityBinding{Pin: pin, Required: refs, RequiredSHA256: protocol.CapabilityRequiredDigest(refs), Evidence: []protocol.ExecutorCapabilityEvidence{}, EvidenceIDs: []string{}}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.InsertRunCapabilityBinding(t.Context(), claimed.Assignment.RunID, legacy)
	}); err != nil {
		t.Fatal(err)
	}
	r.Capabilities = slices.DeleteFunc(slices.Clone(r.Capabilities), func(v string) bool { return v == protocol.CapabilityExecutorDeclarations })
	r.Sequence++
	op = h.versionedHello("linux-1", r)
	if op.Status != protocol.OpSucceeded {
		t.Fatal("legacy feature recovery tightened", op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		run, err := store.GetRun(t.Context(), tx.Q(), claimed.Assignment.RunID)
		if err != nil {
			return err
		}
		lease, err := store.GetLeaseByRun(t.Context(), tx.Q(), run.RunID)
		if err != nil {
			return err
		}
		as, err := buildAssignment(t.Context(), tx.Q(), run, lease, h.app.leaseTTL, h.clock.Now().UnixMilli())
		if err != nil {
			return err
		}
		if as.CapabilityBinding == nil || as.CapabilityBinding.MatchPolicy != "" || as.CapabilityBinding.CurrentMatch != nil {
			t.Fatal("legacy peer received new declaration contract", as.CapabilityBinding)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilityQueueProjectionAgreesWithClaimWithoutNativeInventory(t *testing.T) {
	h, r, _ := capabilitySetup(t)
	task := capabilityTask(t, h)
	h.clock.Advance(6 * time.Minute)
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		row, err := store.GetTask(t.Context(), tx.Q(), task)
		if err != nil {
			return err
		}
		summary, err := views.SummaryWithExecution(t.Context(), tx.Q(), row)
		if err != nil {
			return err
		}
		if summary.Execution == nil || summary.Execution.QueueReason == nil || summary.Execution.QueueReason.Code != protocol.QueueReady {
			t.Fatal("generic projection disagrees with declaration eligibility", summary.Execution)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	explanation, err := h.app.TaskCapabilityExplanation(t.Context(), task, 0, protocol.RoleImplement)
	if err != nil || explanation.State != "eligible" {
		t.Fatal(explanation, err)
	}
	op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: r}, nil), Claim{})
	var result claimResult
	if err := json.Unmarshal(op.Result, &result); err != nil || result.Assignment.TaskID != task || result.Assignment.CapabilityBinding.MatchPolicy != "declaration_v1" {
		t.Fatal(op, err)
	}
}
