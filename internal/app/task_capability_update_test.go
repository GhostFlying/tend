package app

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func putTaskCapability(t *testing.T, h *harness, id, state string, revision int64) {
	t.Helper()
	p := protocol.PutCapabilityPayload{Title: id, State: state, ContractVersion: 1, VerificationPolicy: "declaration_v1"}
	if op := h.exec(owner, h.env("capabilities.put", protocol.Target{Type: "capability", ID: id}, p, rev(revision)), PutCapability{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func taskCapabilityRole(spec *domain.Spec, role protocol.RunRole) *protocol.AgentRequirement {
	if role == protocol.RoleImplement {
		return &spec.AgentSelection.Implementation
	}
	return &spec.AgentSelection.Acceptance
}

func taskCapabilitySpec() domain.Spec {
	s := testSpec
	s.AgentSelection = &protocol.TaskAgentSelection{
		Implementation: protocol.AgentRequirement{CategoryID: "implementer"},
		Acceptance:     protocol.AgentRequirement{CategoryID: "reviewer"},
	}
	return s
}

func TestTaskCapabilityUpdateReferencesWithoutNewMounts(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		for _, change := range []string{"add", "replace"} {
			for _, scenario := range []struct {
				name string
				ref  protocol.CapabilityReference
				code protocol.Code
			}{
				{"unknown_definition", protocol.CapabilityReference{CapabilityID: "missing", DefinitionRevision: 1}, protocol.CodeNotFound},
				{"unknown_revision", protocol.CapabilityReference{CapabilityID: "render", DefinitionRevision: 99}, protocol.CodeNotFound},
				{"retired_current", protocol.CapabilityReference{CapabilityID: "retired", DefinitionRevision: 1}, protocol.CodeInvalidRequest},
				{"retired_revision", protocol.CapabilityReference{CapabilityID: "retired", DefinitionRevision: 2}, protocol.CodeInvalidRequest},
				{"active", protocol.CapabilityReference{CapabilityID: "render", DefinitionRevision: 1}, ""},
				{"active_revision", protocol.CapabilityReference{CapabilityID: "image_gen", DefinitionRevision: 2}, ""},
			} {
				t.Run(string(role)+"/"+change+"/"+scenario.name, func(t *testing.T) {
					h := newHarness(t)
					putTaskCapability(t, h, "render", "active", 0)
					putTaskCapability(t, h, "image_gen", "active", 1)
					putTaskCapability(t, h, "retired", "active", 0)
					putTaskCapability(t, h, "retired", "retired", 1)
					spec := taskCapabilitySpec()
					if change == "replace" {
						taskCapabilityRole(&spec, role).RequiredCapabilities = []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 1}}
					}
					op, task := h.createTask(owner, createTaskPayload{Title: "update required", Spec: spec})
					if op.Status != protocol.OpSucceeded {
						t.Fatal(op)
					}
					before := h.task(task.TaskID)
					original := h.storedSpec(task.TaskID, 1)
					spec = h.storedSpec(task.TaskID, 1)
					taskCapabilityRole(&spec, role).RequiredCapabilities = []protocol.CapabilityReference{scenario.ref}
					tables := []string{"task_specs", "events", "outbox", "plan_actions"}
					counts := make([]int, len(tables))
					for i, table := range tables {
						counts[i] = h.count(table)
					}
					env := h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &spec}, rev(before.ControlRevision))
					op = h.exec(owner, env, UpdateIntent{})
					if scenario.code != "" {
						if op.Status != protocol.OpFailed || op.Error == nil || op.Error.Code != scenario.code {
							t.Fatalf("invalid required update accepted: status=%s error=%+v", op.Status, op.Error)
						}
						if after := h.task(task.TaskID); !reflect.DeepEqual(after, before) {
							t.Fatal("rejected update changed task", after)
						}
						for i, table := range tables {
							if h.count(table) != counts[i] {
								t.Fatal("rejected update changed", table)
							}
						}
					} else {
						if op.Status != protocol.OpSucceeded || h.task(task.TaskID).SpecVersion != 2 {
							t.Fatal("valid update rejected", op)
						}
						saved := h.storedSpec(task.TaskID, 2)
						if !slices.Equal(taskCapabilityRole(&saved, role).RequiredCapabilities, []protocol.CapabilityReference{scenario.ref}) || !saved.Workspace.Equal(original.Workspace) {
							t.Fatal("updated requirement or workspace changed", saved)
						}
					}
					if !reflect.DeepEqual(original, h.storedSpec(task.TaskID, 1)) {
						t.Fatal("historical spec changed")
					}
					if replay := h.exec(owner, env, UpdateIntent{}); !reflect.DeepEqual(replay, op) {
						t.Fatal("original request replay changed", replay, op)
					}
				})
			}
		}
	}
}

func TestTaskCapabilityUpdateRetainsHistoricalReferences(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		for _, change := range []string{"scope", "add_mount", "reorder", "remove", "move_role", "new_revision"} {
			t.Run(string(role)+"/"+change, func(t *testing.T) {
				h := newHarness(t)
				putTaskCapability(t, h, "render", "active", 0)
				spec := taskCapabilitySpec()
				taskCapabilityRole(&spec, role).RequiredCapabilities = []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 1}, {CapabilityID: "render", DefinitionRevision: 1}}
				op, task := h.createTask(owner, createTaskPayload{Title: "retain required", Spec: spec})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				putTaskCapability(t, h, "image_gen", "retired", 1)
				original := h.storedSpec(task.TaskID, 1)
				spec = h.storedSpec(task.TaskID, 1)
				refs := &taskCapabilityRole(&spec, role).RequiredCapabilities
				switch change {
				case "scope":
					spec.Scope = "Keep frozen requirements while clarifying scope"
				case "add_mount":
					spec.Workspace.Mounts = append(spec.Workspace.Mounts, domain.Mount{Repo: "douyin"})
				case "reorder":
					slices.Reverse(*refs)
				case "remove":
					*refs = nil
				case "move_role":
					other := protocol.RoleImplement
					if role == other {
						other = protocol.RoleAccept
					}
					taskCapabilityRole(&spec, other).RequiredCapabilities = slices.Clone(*refs)
					*refs = nil
				case "new_revision":
					(*refs)[0].DefinitionRevision = 2
				}
				op = h.exec(owner, h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &spec}, rev(h.task(task.TaskID).ControlRevision)), UpdateIntent{})
				if change == "move_role" || change == "new_revision" {
					if op.Status != protocol.OpFailed || op.Error == nil || op.Error.Code != protocol.CodeInvalidRequest || h.task(task.TaskID).SpecVersion != 1 {
						t.Fatal("retirement bypassed by new role/revision", op)
					}
				} else if op.Status != protocol.OpSucceeded {
					t.Fatal("unchanged historical reference blocked update", op)
				}
				if !reflect.DeepEqual(original, h.storedSpec(task.TaskID, 1)) {
					t.Fatal("historical required changed")
				}
			})
		}
	}
}

func TestTaskCapabilityUpdatePreservesInFlightPinAndRequestIdentity(t *testing.T) {
	h, runtime, pin := capabilitySetup(t)
	task := capabilityTask(t, h)
	as, ok := h.claimWith("linux-1", h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: runtime}, nil))
	if !ok || as.Execution == nil || *as.Execution != pin {
		t.Fatal("missing original assignment", as)
	}
	readFrozen := func() []string {
		t.Helper()
		var spec, execution, binding string
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			spec, err = store.GetSpec(t.Context(), tx.Q(), task, 1)
			if err != nil {
				return err
			}
			if err := tx.Q().QueryRowContext(t.Context(), `SELECT pin_json FROM run_agent_executions WHERE run_id=?`, as.RunID).Scan(&execution); err != nil {
				return err
			}
			return tx.Q().QueryRowContext(t.Context(), `SELECT binding_json FROM run_capability_bindings WHERE run_id=?`, as.RunID).Scan(&binding)
		}); err != nil {
			t.Fatal(err)
		}
		return []string{spec, execution, binding}
	}
	frozen := readFrozen()
	putTaskCapability(t, h, "image_gen", "retired", 1)
	spec := h.storedSpec(task, 1)
	spec.Scope = "Clarify the next agreement while retaining the original run"
	before := h.task(task)
	env := h.env("tasks.update_intent", taskTarget(task), updateIntentPayload{Spec: &spec}, rev(before.ControlRevision))
	op := h.exec(owner, env, UpdateIntent{})
	if op.Status != protocol.OpSucceeded || h.task(task).SpecVersion != 2 || !slices.Equal(frozen, readFrozen()) {
		t.Fatal("update lost original frozen run", op)
	}
	if replay := h.exec(owner, env, UpdateIntent{}); !reflect.DeepEqual(replay, op) {
		t.Fatal("original request replay changed", replay)
	}
	if stale := h.exec(owner, h.env("tasks.update_intent", taskTarget(task), updateIntentPayload{Spec: &spec}, rev(before.ControlRevision)), UpdateIntent{}); stale.Error == nil || stale.Error.Code != protocol.CodeStaleRevision {
		t.Fatal("stale CAS accepted", stale)
	}
	spec.Scope = "Different payload cannot reuse the original request"
	env.Payload, _ = json.Marshal(updateIntentPayload{Spec: &spec})
	if _, err := h.app.Execute(t.Context(), owner, env, UpdateIntent{}); protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatal("request identity changed", err)
	}
	if !slices.Equal(frozen, readFrozen()) || h.task(task).SpecVersion != 2 {
		t.Fatal("replay/conflict changed frozen run or spec")
	}
}
