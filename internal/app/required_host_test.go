package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestRequiredHostCreationAndProjection(t *testing.T) {
	h := newDeliverableHarness(t)
	spec := temporaryReportSpec()
	spec.Requirements.HostID = "absent"
	op, _ := h.createTask(owner, createTaskPayload{Title: "wrong host", Spec: spec})
	expectWorkCode(t, op, protocol.CodeInvalidRequest)
	spec.Requirements.HostID = "linux-1"
	task := h.createReportTask("pinned", spec)
	if h.storedSpec(task.TaskID, 1).Requirements.HostID != "linux-1" {
		t.Fatal("lost host requirement")
	}
	raw, err := workerSpec(spec, protocol.RoleImplement)
	if err != nil || strings.Contains(string(raw), "host_id") || spec.Requirements.HostID != "linux-1" {
		t.Fatalf("wire projection: %s %v", raw, err)
	}
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		err := checkRequiredHost(context.Background(), tx.Q(), spec, &store.WorkspaceRow{HostID: "other"})
		if err == nil || !strings.Contains(err.Error(), "conflicts with workspace") {
			t.Fatalf("missing conflict: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRequiredHostRetiredRejected(t *testing.T) {
	h := newDeliverableHarness(t)
	if err := h.store.Write(context.Background(), func(tx *store.Tx) error { return tx.RetireHostsExcept(context.Background(), nil, 0) }); err != nil {
		t.Fatal(err)
	}
	spec := domain.Spec{Requirements: domain.Requirements{HostID: "linux-1"}}
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		err := checkRequiredHost(context.Background(), tx.Q(), spec, nil)
		if err == nil || !strings.Contains(err.Error(), "retired") {
			t.Fatalf("retired allowed: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRequiredHostPreventsWrongClaimAndWorkspaceMove(t *testing.T) {
	h := newHarness(t)
	config := testConfig()
	extra := config.Hosts[0]
	extra.HostID = "linux-2"
	config.Hosts = append(config.Hosts, extra)
	h.syncConfig(config)
	spec := testSpec
	spec.Requirements.HostID = "linux-2"
	op, task := h.createTask(owner, createTaskPayload{Title: "specific host", Spec: spec})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, ok, _ := h.claim("linux-1"); ok {
		t.Fatal("wrong host claimed task")
	}
	assigned, ok, _ := h.claim("linux-2")
	if !ok || assigned.TaskID != task.TaskID {
		t.Fatalf("required host did not claim: %+v", assigned)
	}
	spec.Requirements.HostID = "linux-1"
	op = h.exec(owner, h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &spec}, rev(h.task(task.TaskID).ControlRevision)), UpdateIntent{})
	expectWorkCode(t, op, protocol.CodeInvalidRequest)
	if !strings.Contains(op.Error.Message, "conflicts with workspace") {
		t.Fatal(op.Error)
	}
}

func TestRequiredHostLegacySpecAndRunnerRequirements(t *testing.T) {
	var old domain.Spec
	if err := json.Unmarshal([]byte(`{"goal":"legacy","requirements":{"os":"linux","labels":["build"]}}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Requirements.HostID != "" {
		t.Fatal("historical spec gained host constraint")
	}
	pinned := old
	pinned.Requirements.HostID = "machine"
	if pinned.Requirements.Equal(old.Requirements) {
		t.Fatal("host constraint ignored by semantic equality")
	}
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		raw, err := workerSpec(pinned, role)
		if err != nil {
			t.Fatal(err)
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		var legacy struct {
			OS     string   `json:"os,omitempty"`
			Labels []string `json:"labels,omitempty"`
		}
		decoder := json.NewDecoder(strings.NewReader(string(envelope["requirements"])))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&legacy); err != nil {
			t.Fatalf("legacy requirements rejected: %v", err)
		}
		if legacy.OS != "linux" || len(legacy.Labels) != 1 || legacy.Labels[0] != "build" {
			t.Fatalf("requirements lost: %+v", legacy)
		}
	}
}

func TestRequiredHostLossRejectsLegacyStartOverWithoutAbandoningWorkspace(t *testing.T) {
	h := newDeliverableHarness(t)
	spec := temporaryReportSpec()
	spec.Requirements.HostID = "linux-1"
	task := h.createReportTask("host-bound", spec)
	as, ok, _ := h.claimDeliverableRun()
	if !ok {
		t.Fatal("missing assignment")
	}
	h.reportDirectory(as, t.TempDir())
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Preserve local investigation"}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.withHosts("linux-2")
	h.checkWorkspaces()
	questions := h.openDecisions(task.TaskID)
	if len(questions) != 1 || strings.Contains(questions[0].OptionsJSON, domain.StartOverOption) || !strings.Contains(questions[0].Question, "requirements.host_id") {
		t.Fatal(questions)
	}
	original := h.resourceForTask(task.TaskID)
	question := questions[0]
	// Older servers persisted an unsafe option; keep its answer without applying it.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE decisions SET options_json=?,effects_json=? WHERE decision_id=?`, `["wait for the host","start over on another host","cancel the task"]`, `{"start over on another host":"start_over","cancel the task":"cancel"}`, question.DecisionID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	answer := h.answer(question, domain.StartOverOption)
	if answer.Effect == nil || answer.Effect.Applied || !strings.Contains(answer.Effect.Reason, "requirements.host_id") {
		t.Fatal(answer)
	}
	got := h.resourceForTask(task.TaskID)
	if got.WorkspaceID != original.WorkspaceID || got.Status != original.Status || got.Path != original.Path || got.HostID != original.HostID {
		t.Fatal("workspace changed", got)
	}
	if h.task(task.TaskID).BlockReason != protocol.BlockWorkspaceUnavailable {
		t.Fatal("unsafe option cleared block")
	}
	replacement := h.openDecisions(task.TaskID)
	if len(replacement) != 1 || replacement[0].DecisionID == question.DecisionID || strings.Contains(replacement[0].OptionsJSON, domain.StartOverOption) {
		t.Fatal(replacement)
	}
	h.withHosts("linux-1", "linux-2")
	h.checkWorkspaces()
	if h.task(task.TaskID).BlockReason != protocol.BlockNone || len(h.openDecisions(task.TaskID)) != 0 {
		t.Fatal("host return did not resolve wait")
	}
	if h.resourceForTask(task.TaskID).WorkspaceID != original.WorkspaceID {
		t.Fatal("host return changed resource")
	}
	resumed, ok, _ := h.claimDeliverableRun()
	if !ok || resumed.TaskID != task.TaskID || resumed.Role != protocol.RoleAccept || resumed.Workspace.ID != original.WorkspaceID {
		t.Fatalf("host return failed to resume original resource: %+v %v", resumed, ok)
	}
}
