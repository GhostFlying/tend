package app

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Exercise the authenticated report and reconnect paths with a historical
// binding, rather than testing a declaration matcher against fabricated IDs.
func TestCapabilityLegacyRevalidationSurvivesDeclarationServer(t *testing.T) {
	h, r, pin := capabilitySetup(t)
	spec := testSpec
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}
	_, task := h.createTask(owner, createTaskPayload{Title: "legacy recovery", Spec: spec})
	target := protocol.Target{Type: "host", ID: "linux-1"}
	op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, target, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: r}, nil), Claim{})
	var claimed claimResult
	if err := json.Unmarshal(op.Result, &claimed); err != nil || claimed.Assignment.TaskID != task.TaskID {
		t.Fatal(op, err)
	}
	old := capabilityEvidence(h, r, pin, 1)
	old.EvidenceID, old.RequestID = "historical-evidence", "historical-request"
	refs := []protocol.CapabilityReference{old.Capability}
	binding := protocol.RunCapabilityBinding{Pin: pin, Required: refs, RequiredSHA256: protocol.CapabilityRequiredDigest(refs), Evidence: []protocol.ExecutorCapabilityEvidence{old}, EvidenceIDs: []string{old.EvidenceID}}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertCapabilityEvidence(t.Context(), old); err != nil {
			return err
		}
		return tx.InsertRunCapabilityBinding(t.Context(), claimed.Assignment.RunID, binding)
	}); err != nil {
		t.Fatal(err)
	}
	original, _ := json.Marshal(binding)
	r.Capabilities = slices.DeleteFunc(slices.Clone(r.Capabilities), func(v string) bool { return v == protocol.CapabilityExecutorDeclarations })
	h.clock.Advance(6 * time.Minute)
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	fresh := capabilityEvidence(h, r, pin, 2)
	report := h.env(protocol.OpRunnerCapabilityEvidence, target, protocol.ReportCapabilityEvidencePayload{Runtime: r, Evidence: fresh}, rev(1))
	for range 2 { // Replay must not append another recovery receipt.
		if op := h.exec(runner("linux-1"), report, ReportCapabilityEvidence{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	check := func(want bool) {
		t.Helper()
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			b, err := store.GetRunCapabilityBinding(t.Context(), tx.Q(), claimed.Assignment.RunID)
			if err != nil {
				return err
			}
			body, _ := json.Marshal(b)
			if string(body) != string(original) {
				t.Fatal("historical binding rewritten")
			}
			var n int
			if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM run_capability_revalidations WHERE run_id=?`, claimed.Assignment.RunID).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				t.Fatal("revalidation replay duplicated", n)
			}
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
			if as.CapabilityBinding == nil || as.CapabilityBinding.CurrentMatch != nil || as.CapabilityBinding.MatchPolicy != "" {
				t.Fatal("legacy wire changed", as.CapabilityBinding)
			}
			recovery := as.CapabilityBinding.Revalidation
			if want {
				if recovery == nil || recovery.RequestID != report.RequestID || recovery.Pin != pin || !slices.Equal(recovery.EvidenceIDs, []string{"ce_" + report.RequestID}) || len(recovery.Evidence) != 1 {
					t.Fatal("fresh legacy recovery missing", recovery)
				}
			} else if recovery != nil {
				t.Fatal("stale recovery returned", recovery)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	r.Sequence++
	hello := h.versionedHello("linux-1", r)
	var resumed protocol.HelloResult
	if err := json.Unmarshal(hello.Result, &resumed); err != nil || hello.Status != protocol.OpSucceeded || len(resumed.Reassigned) != 1 || resumed.Reassigned[0].CapabilityBinding.Revalidation == nil {
		t.Fatal("legacy Hello omitted recovery", hello, err)
	}
	check(true)
	h.clock.Advance(6 * time.Minute)
	check(false)
}

func TestCapabilityLegacyRecoveryRejectsChangedBindingOrIncarnation(t *testing.T) {
	for _, change := range []string{"pin", "incarnation", "generation", "sequence", "simulation", "session"} {
		t.Run(change, func(t *testing.T) {
			h, r, pin := capabilitySetup(t)
			e := capabilityEvidence(h, r, pin, 2)
			e.EvidenceID = "fresh"
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				fact := e
				switch change {
				case "pin":
					fact.Pin.Runtime.Revision++
				case "incarnation":
					fact.InstanceID = "different"
				case "generation":
					fact.Generation++
				case "sequence":
					// A newer unavailable observation must supersede the old success.
					stale := e
					stale.Sequence = 1
					stale.EvidenceID = "stale"
					if err := tx.InsertCapabilityEvidence(t.Context(), stale); err != nil {
						return err
					}
					fact.State = "unavailable"
					fact.Reason = "tool_absent"
				case "simulation":
					fact.Kind = "simulation"
					fact.State = "unverified"
					fact.Reason = "probe_timeout"
				case "session":
					fact.Scope = "session"
					fact.SessionID = "session"
				}
				return tx.InsertCapabilityEvidence(t.Context(), fact)
			}); err != nil {
				t.Fatal(err)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				match, err := currentRunCapabilityMatch(t.Context(), tx.Q(), protocol.RunCapabilityBinding{Pin: pin, Required: []protocol.CapabilityReference{e.Capability}}, *r, h.clock.Now().UnixMilli())
				if err == nil && match.State == "eligible" {
					t.Fatal("changed historical evidence authorized recovery", match)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCapabilityOrdinaryWaitProjectionAndClaim(t *testing.T) {
	for _, wait := range []string{"host_capacity", "adapter_unavailable", "heartbeat_expired", "reviewer_unavailable"} {
		t.Run(wait, func(t *testing.T) {
			h, r, pin := capabilitySetup(t)
			task := capabilityTask(t, h)
			switch wait {
			case "host_capacity":
				config := testConfig()
				config.Hosts[0].Slots = 0
				h.syncConfig(config)
			case "adapter_unavailable":
				r.AgentExecutors[0].Available = false
				r.AgentExecutors[0].Reason = "adapter_initialization_failed"
			case "reviewer_unavailable":
				r.AgentExecutors[1].Available = false
				r.AgentExecutors[1].Reason = "reviewer_initialization_failed"
			}
			r.Sequence++
			if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if wait == "heartbeat_expired" {
				h.clock.Advance(time.Minute)
			}
			explanation, err := h.app.TaskCapabilityExplanation(t.Context(), task, 0, protocol.RoleImplement)
			if err != nil || explanation.State != "pending" {
				t.Fatal("recoverable wait misclassified", explanation, err)
			}
			if wait == "adapter_unavailable" && strings.Contains(strings.Join(explanation.Reasons, " "), "capability_unavailable:") {
				t.Fatal("adapter wait misreported as missing capability", explanation.Reasons)
			}
			found := false
			for _, c := range explanation.Candidates {
				if c.Identity == pin.Identity {
					found = true
					if c.Current.State != "eligible" || c.SchedulingReason == nil {
						t.Fatal("declaration/ordinary wait lost", c)
					}
					if wait == "host_capacity" && c.SchedulingReason.Code != protocol.QueueHostCapacity {
						t.Fatal(c)
					}
					if wait == "adapter_unavailable" && !strings.Contains(c.SchedulingReason.Message, "adapter_initialization_failed") {
						t.Fatal(c)
					}
				}
			}
			if !found {
				t.Fatal("unavailable executor identity discarded", explanation)
			}
			page, err := h.app.CapabilityAgents(t.Context(), protocol.AgentCapabilityQuery{Environment: "linux-1", Agent: pin.Identity.LocalID})
			if err != nil || len(page.Items) != 1 {
				t.Fatal(page, err)
			}
			if wait == "adapter_unavailable" || wait == "heartbeat_expired" {
				if page.Items[0].Pin == nil || page.Items[0].Current.State != "pending" || page.Items[0].SchedulingReason == nil {
					t.Fatal("catalog lost readiness", page)
				}
			}
			// Claim uses its normal current runtime path. Host and adapter capacity
			// waits must never create an assignment.
			claim := func() claimResult {
				t.Helper()
				op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: r}, nil), Claim{})
				var result claimResult
				if err := json.Unmarshal(op.Result, &result); err != nil {
					t.Fatal(op, err)
				}
				return result
			}
			if result := claim(); result.Assignment.RunID != "" {
				t.Fatal("waiting executor claimed task", result)
			}
			// Restore only ordinary availability; no evidence/verification is added.
			if wait == "host_capacity" {
				h.syncConfig(testConfig())
			}
			for i := range r.AgentExecutors {
				r.AgentExecutors[i].Available = true
				r.AgentExecutors[i].Reason = ""
			}
			r.Sequence++
			if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			explanation, err = h.app.TaskCapabilityExplanation(t.Context(), task, 0, protocol.RoleImplement)
			if err != nil || explanation.State != "eligible" {
				t.Fatal("readiness restoration not reflected", explanation, err)
			}
			if result := claim(); result.Assignment.TaskID != task {
				t.Fatal("restored executor could not claim", result)
			}
		})
	}
}

func TestCapabilityCapacityWaitDoesNotRelaxFrozenIdentity(t *testing.T) {
	h, _, _ := capabilitySetup(t)
	config := testConfig()
	config.Hosts[0].Slots = 0
	h.syncConfig(config)
	spec := testSpec
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer", AgentID: "linux-1/different", RequiredCapabilities: []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 1}}}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}
	_, task := h.createTask(owner, createTaskPayload{Title: "frozen identity", Spec: spec})
	explanation, err := h.app.TaskCapabilityExplanation(t.Context(), task.TaskID, 0, protocol.RoleImplement)
	if err != nil || explanation.State != "unavailable" {
		t.Fatal("capacity hid identity mismatch", explanation, err)
	}
}

func TestCapabilityRuntimeCapacityWaitRetainsDeclarationAndClaimBudget(t *testing.T) {
	h, r, pin := capabilitySetup(t)
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAdaptiveConcurrency)
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 1, Max: 4})
	concurrencyConfigure(t, h, "bind", "shared", 1, protocol.ConfigureConcurrencyPayload{EnvironmentID: "linux-1", RuntimeID: pin.Runtime.RuntimeID})
	first := capabilityTask(t, h)
	claim := func() protocol.Assignment {
		t.Helper()
		op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: r}, nil), Claim{})
		var result claimResult
		if err := json.Unmarshal(op.Result, &result); err != nil {
			t.Fatal(op, err)
		}
		return result.Assignment
	}
	if as := claim(); as.TaskID != first {
		t.Fatal(as)
	}
	second := capabilityTask(t, h)
	explanation, err := h.app.TaskCapabilityExplanation(t.Context(), second, 0, protocol.RoleImplement)
	if err != nil || explanation.State != "pending" || !strings.Contains(strings.Join(explanation.Reasons, " "), "runtime concurrency capacity") {
		t.Fatal(explanation, err)
	}
	if as := claim(); as.RunID != "" {
		t.Fatal("shared budget bypassed", as)
	}
	concurrencyConfigure(t, h, "unbind", "shared", 2, protocol.ConfigureConcurrencyPayload{EnvironmentID: "linux-1", RuntimeID: pin.Runtime.RuntimeID})
	explanation, err = h.app.TaskCapabilityExplanation(t.Context(), second, 0, protocol.RoleImplement)
	if err != nil || explanation.State != "eligible" {
		t.Fatal(explanation, err)
	}
	if as := claim(); as.TaskID != second {
		t.Fatal(as)
	}
}
