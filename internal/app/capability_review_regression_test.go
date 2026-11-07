package app

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func addUnobservedCapabilityHost(t *testing.T, h *harness) {
	t.Helper()
	config := testConfig()
	host := config.Hosts[0]
	host.HostID = "never-connected"
	config.Hosts = append(config.Hosts, host)
	if op := h.syncConfig(config); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		_, err := store.GetHostRuntime(t.Context(), tx.Q(), host.HostID)
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("expected an unobserved host, got %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilityExplanationPreservesHealthyClaimWithUnobservedHost(t *testing.T) {
	h, r, pin := capabilitySetup(t)
	task := capabilityTask(t, h)
	addUnobservedCapabilityHost(t, h)
	explanation, err := h.app.TaskCapabilityExplanation(t.Context(), task, 0, protocol.RoleImplement)
	if err != nil || explanation.State != "eligible" {
		t.Fatal("unobserved host hid healthy candidate", explanation, err)
	}
	if !strings.Contains(strings.Join(explanation.Reasons, " "), "runner runtime has not been observed for host never-connected") {
		t.Fatal("unobserved host reason missing", explanation.Reasons)
	}
	found := false
	for _, c := range explanation.Candidates {
		if c.Identity.ExecutionEnvironmentID == "never-connected" {
			t.Fatal("invented executor for unobserved host", c)
		}
		if c.Identity == pin.Identity && c.Current.State == "eligible" && c.SchedulingReason == nil {
			found = true
		}
	}
	if !found {
		t.Fatal("healthy frozen executor missing", explanation)
	}
	op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: r}, nil), Claim{})
	var result claimResult
	if err := json.Unmarshal(op.Result, &result); err != nil || op.Status != protocol.OpSucceeded || result.Assignment.TaskID != task || result.Assignment.Execution == nil || *result.Assignment.Execution != pin {
		t.Fatal("explanation and claim disagree", op, err)
	}
}

func TestCapabilityWaitNoticeSurvivesUnobservedHost(t *testing.T) {
	h, _, _ := capabilitySetup(t)
	task := capabilityTask(t, h)
	addUnobservedCapabilityHost(t, h)
	h.clock.Advance(11 * time.Minute)
	for range 2 {
		if err := h.app.SweepCapabilityWaits(t.Context()); err != nil {
			t.Fatal("unobserved host blocked notification", err)
		}
	}
	explanation, err := h.app.TaskCapabilityExplanation(t.Context(), task, 0, protocol.RoleImplement)
	if err != nil || explanation.State != "pending" {
		t.Fatal("expired healthy host must still wait", explanation, err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		for _, check := range []struct {
			query string
			want  int
		}{
			{`SELECT COUNT(*) FROM capability_wait_notices WHERE task_id=?`, 1},
			{`SELECT COUNT(*) FROM events WHERE object_id=? AND kind='task.capability_wait_notified'`, 1},
		} {
			var n int
			if err := tx.Q().QueryRowContext(t.Context(), check.query, task).Scan(&n); err != nil {
				return err
			}
			if n != check.want {
				t.Fatalf("%s: got %d, want %d", check.query, n, check.want)
			}
		}
		var n int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM attentions WHERE json_extract(body_json,'$.title') LIKE 'Required capability%'`).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatal("attention missing or duplicated", n)
		}
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sqlite_master WHERE name='executor_capability_evidence'`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("notification created native evidence", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilityAcceptancePrerequisiteWaitNotice(t *testing.T) {
	for _, scenario := range []string{"acceptance_only", "both_roles", "implementation_notified", "acceptance_available", "waived"} {
		t.Run(scenario, func(t *testing.T) {
			h, r, _ := capabilitySetup(t)
			ref := protocol.CapabilityReference{CapabilityID: "image_gen", DefinitionRevision: 1}
			spec := testSpec
			spec.AgentSelection = &protocol.TaskAgentSelection{
				Implementation: protocol.AgentRequirement{CategoryID: "implementer"},
				Acceptance:     protocol.AgentRequirement{CategoryID: "reviewer", RequiredCapabilities: []protocol.CapabilityReference{ref}},
			}
			want := 1
			if scenario == "both_roles" || scenario == "implementation_notified" {
				spec.AgentSelection.Implementation.RequiredCapabilities = []protocol.CapabilityReference{ref}
				want = 2
			}
			if scenario == "acceptance_available" {
				// This category has an explicit declaration; no evidence is added.
				spec.AgentSelection.Acceptance.CategoryID = "implementer"
				want = 0
			}
			var op protocol.Operation
			var task taskResult
			if scenario == "waived" {
				issue := policyIssue(t, h, false)
				spec.Delivery = ""
				spec.Deliverables = []string{domain.DeliveryReport}
				op = h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "waived acceptance prerequisite", Spec: spec}, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
				if err := json.Unmarshal(op.Result, &task); err != nil {
					t.Fatal(op, err)
				}
				want = 0
			} else {
				op, task = h.createTask(owner, createTaskPayload{Title: "acceptance prerequisite", Spec: spec})
			}
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if err := h.app.SweepCapabilityWaits(t.Context()); err != nil {
				t.Fatal(err)
			}
			if n := h.count("capability_wait_notices"); n != 0 {
				t.Fatal("notified before deadline", n)
			}
			wantNew := want
			if scenario == "implementation_notified" {
				// A durable notice from the old sweep must not suppress the
				// missing acceptance notice on the next maintenance pass.
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `INSERT INTO capability_wait_notices(task_id,spec_version,role,request_id,created_at) VALUES(?,1,'implement','historical-notice',?)`, task.TaskID, h.clock.Now().UnixMilli())
					return err
				}); err != nil {
					t.Fatal(err)
				}
				wantNew--
			}
			h.clock.Advance(11 * time.Minute)
			r.Sequence++
			if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			explanation, err := h.app.TaskCapabilityExplanation(t.Context(), task.TaskID, 0, protocol.RoleAccept)
			if err != nil || !slices.Equal(explanation.Required, []protocol.CapabilityReference{ref}) || scenario != "waived" && (explanation.State == "eligible") != (want == 0) {
				t.Fatal("acceptance prerequisite explanation", explanation, err)
			}
			if want != 0 {
				op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: r}, nil), Claim{})
				var result claimResult
				if err := json.Unmarshal(op.Result, &result); err != nil || op.Status != protocol.OpSucceeded || result.Assignment.RunID != "" {
					t.Fatal("undeclared reviewer allowed first allocation", op, err)
				}
			}
			// Concurrent maintenance and repeated sweeps must persist only one
			// notice per blocking role, without allocating another run or probe.
			results := make(chan error, 2)
			for range 2 {
				go func() { results <- h.app.SweepCapabilityWaits(t.Context()) }()
			}
			for range 2 {
				if err := <-results; err != nil {
					t.Fatal(err)
				}
			}
			if err := h.app.SweepCapabilityWaits(t.Context()); err != nil {
				t.Fatal(err)
			}
			if n := h.count("capability_wait_notices"); n != want {
				t.Fatalf("got %d durable notices, want %d", n, want)
			}
			if n := h.count("attentions"); n != wantNew {
				t.Fatalf("got %d attentions, want %d", n, wantNew)
			}
			if h.count("runs") != 0 {
				t.Fatal("sweep created execution or native evidence")
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var n int
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM capability_wait_notices WHERE task_id=? AND role='accept'`, task.TaskID).Scan(&n); err != nil {
					return err
				}
				if (n == 1) != (want != 0) {
					t.Fatal("notice recorded against the wrong role", n)
				}
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM events WHERE object_id=? AND kind='task.capability_wait_notified' AND json_extract(body_json,'$.explanation.role')='accept' AND json_extract(body_json,'$.explanation.required[0].capability_id')='image_gen'`, task.TaskID).Scan(&n); err != nil {
					return err
				}
				if (n == 1) != (want != 0) {
					t.Fatal("event lost acceptance requirements", n)
				}
				body, err := store.GetSpec(t.Context(), tx.Q(), task.TaskID, 1)
				if err != nil {
					return err
				}
				var frozen struct {
					AgentSelection *protocol.TaskAgentSelection `json:"agent_selection"`
				}
				if err := json.Unmarshal([]byte(body), &frozen); err != nil {
					return err
				}
				if !frozen.AgentSelection.Equal(*spec.AgentSelection) {
					t.Fatal("sweep rewrote role requirements", frozen.AgentSelection)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if want == 0 {
				op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: r}, nil), Claim{})
				var result claimResult
				if err := json.Unmarshal(op.Result, &result); err != nil || op.Status != protocol.OpSucceeded || result.Assignment.TaskID != task.TaskID || result.Assignment.Role != protocol.RoleImplement || result.Assignment.CapabilityBinding != nil {
					t.Fatal("acceptance label leaked into implementation", op, err)
				}
			}
		})
	}
}

// Exercise the authenticated report and reconnect paths with a historical
// binding, rather than testing a declaration matcher against fabricated IDs.

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
