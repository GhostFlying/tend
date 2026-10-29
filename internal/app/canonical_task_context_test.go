package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// These task/spec/primary rows are explicit store-fact fixtures for read/context
// consumers. The internal owner execution path has separate app-chain tests.
func canonicalTaskReadFixture(t *testing.T, h *harness, issue protocol.Issue) string {
	t.Helper()
	id := h.ids.New(protocol.PrefixTask)
	raw, err := json.Marshal(testSpec)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertTask(t.Context(), store.TaskRow{TaskID: id, Title: "No-stage execution", Project: "default", Lifecycle: protocol.TaskDraft, ControlMode: protocol.ControlManaged, ControlRevision: 1, SpecVersion: 1, Rating: protocol.RatingP1, BlockReason: protocol.BlockNone}); err != nil {
			return err
		}
		if err := tx.InsertSpec(t.Context(), id, 1, string(raw), "", 1); err != nil {
			return err
		}
		return tx.InsertTaskIssueOwner(t.Context(), id, issue.IssueID)
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCanonicalTaskNoStageDetailContextAndAcceptanceIsolation(t *testing.T) {
	h := newHarness(t)
	parent := createCanonicalOwnerTestIssue(t, h)
	p := canonicalOwnerIssueCreate{Plan: protocol.IssuePlan{Title: "Child", Project: "default", Intent: "Child goal", Background: []protocol.SharedFact{{Kind: protocol.SharedFactUserConstraint, Text: "Preserve the public API", Source: "owner brief"}}, Stages: []protocol.IssueStage{}}, ParentIssueID: parent.IssueID}
	op := h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, p, nil), CanonicalOwnerIssueCommand{Action: "create"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var child protocol.Issue
	if err := json.Unmarshal(op.Result, &child); err != nil {
		t.Fatal(err)
	}
	task := canonicalTaskReadFixture(t, h, child)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		detail, err := views.Task(t.Context(), tx.Q(), task)
		if err != nil {
			return err
		}
		if len(detail.WorkItemIDs) != 1 || detail.WorkItemIDs[0] != child.IssueID {
			t.Fatalf("detail=%+v", detail.WorkItemIDs)
		}
		issue, err := views.Issue(t.Context(), tx.Q(), child.IssueID)
		if err != nil {
			return err
		}
		if len(issue.Stages) != 0 || len(issue.Tasks) != 1 || issue.Tasks[0].TaskID != task || issue.Column != "active" || issue.Execution == nil {
			t.Fatalf("view=%+v", issue)
		}
		spec := testSpec
		spec.ImplementationContext = "PRIVATE_EXECUTION_RATIONALE"
		if err := refreshTaskWorkContext(t.Context(), tx.Q(), task, &spec, nil); err != nil {
			return err
		}
		if len(spec.SharedContext) != 2 || spec.SharedContext[0].WorkItemID != child.IssueID || spec.SharedContext[1].WorkItemID != parent.IssueID || spec.SharedContext[0].ContextDigest == "" || spec.SharedContext[0].Revision != 1 {
			t.Fatalf("frozen=%+v", spec.SharedContext)
		}
		for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
			raw, err := workerSpec(spec, role)
			if err != nil {
				return err
			}
			if !strings.Contains(string(raw), "Preserve the public API") || !strings.Contains(string(raw), "Child goal") {
				t.Fatalf("%s lost shared facts: %s", role, raw)
			}
			if strings.Contains(string(raw), "PRIVATE_EXECUTION_RATIONALE") != (role == protocol.RoleImplement) {
				t.Fatalf("%s narrative projection=%s", role, raw)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalTaskSourceRefreshUsesPrimaryNotResultReference(t *testing.T) {
	h := newHarness(t)
	primary := createCanonicalOwnerTestIssue(t, h)
	reference := createCanonicalOwnerTestIssue(t, h)
	task := canonicalTaskReadFixture(t, h, primary)
	var frozen domain.Spec
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_item_task_links(work_item_id,stage_id,task_id)VALUES(?,'evidence',?)`, reference.IssueID, task); err != nil {
			return err
		}
		if _, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: "msg_read_source", Author: "owner", Body: "Do not deploy"}); err != nil {
			return err
		}
		if err := bindSource(t.Context(), tx, "msg_read_source", "task", task); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_item_context_facts(fact_id,work_item_id,project,scope,kind,body,source_message_id,attribution,created_at)VALUES('fact_read_source',?,'default','work_item','constraint','Do not deploy','msg_read_source','owner',1)`, primary.IssueID); err != nil {
			return err
		}
		frozen = testSpec
		return refreshTaskWorkContext(t.Context(), tx.Q(), task, &frozen, nil)
	}); err != nil {
		t.Fatal(err)
	}
	if len(frozen.SharedContext) != 1 || frozen.SharedContext[0].WorkItemID != primary.IssueID {
		t.Fatalf("reference became context: %+v", frozen.SharedContext)
	}
	old := frozen.SharedContext[0].ContextDigest
	raw, err := json.Marshal(frozen)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertSpec(t.Context(), task, 2, string(raw), "", 2); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE tasks SET spec_version=2 WHERE task_id=?`, task)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err = h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		blockers, err := store.TaskContextBlockers(t.Context(), tx.Q(), primary.IssueID)
		if err != nil {
			return err
		}
		if len(blockers) != 0 {
			t.Fatalf("unchanged agreement blocked=%+v", blockers)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE conversation_messages SET body='Deploy only after review' WHERE message_id='msg_read_source'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err = h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		blockers, err := store.TaskContextBlockers(t.Context(), tx.Q(), primary.IssueID)
		if err != nil {
			return err
		}
		if len(blockers) != 1 || blockers[0].ObjectID != task || blockers[0].SpecVersion != 2 {
			t.Fatalf("source edit not fenced=%+v", blockers)
		}
		other, err := store.TaskContextBlockers(t.Context(), tx.Q(), reference.IssueID)
		if err != nil {
			return err
		}
		if len(other) != 0 {
			t.Fatalf("reference controls agreement=%+v", other)
		}
		next := testSpec
		if err := refreshTaskWorkContext(t.Context(), tx.Q(), task, &next, frozen.SharedContext); err != nil {
			return err
		}
		if next.SharedContext[0].ContextDigest == old {
			t.Fatal("source edit lost from refreshed digest")
		}
		detail, err := views.Task(t.Context(), tx.Q(), task)
		if err != nil {
			return err
		}
		if len(detail.WorkItemIDs) != 1 || detail.WorkItemIDs[0] != primary.IssueID {
			t.Fatalf("reference became authority=%+v", detail.WorkItemIDs)
		}
		var n int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM conversation_bindings WHERE message_id='msg_read_source' AND object_type='work_item' AND object_id=?`, reference.IssueID).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("source bound to reference")
		}
		stored, err := store.GetSpec(t.Context(), tx.Q(), task, 2)
		if err != nil {
			return err
		}
		if stored != string(raw) {
			t.Fatal("read rewrote frozen agreement")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalTaskNoStageObservationUsesFrozenReadScope(t *testing.T) {
	h := newHarness(t)
	rt := testRuntime("observation-primary")
	rt.Capabilities = append(rt.Capabilities, protocol.CapabilityWorkerObservation)
	if op := h.versionedHello("linux-1", rt); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue := createCanonicalOwnerTestIssue(t, h)
	task := canonicalTaskReadFixture(t, h, issue)
	input := testSpec
	input.Observation = &protocol.ObservationScope{WorkItemIDs: []string{issue.IssueID}}
	op, _ := h.createTask(owner, createTaskPayload{Title: "Observe read model", Spec: input})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	assignment, ok := h.claimInputRun(rt)
	if !ok || assignment.TaskID == task {
		t.Fatal("legacy observation fixture was not the only executable task")
	}
	h.runAs(assignment, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	observation, err := h.app.ReadRunObservation(t.Context(), runPrincipal(assignment.RunID, assignment.Role))
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.WorkItems) != 1 || len(observation.WorkItems[0].Tasks) != 1 || observation.WorkItems[0].Tasks[0].TaskID != task || len(observation.WorkItems[0].Stages) != 0 {
		t.Fatalf("no-stage observation=%+v", observation)
	}
	raw, _ := json.Marshal(observation)
	if strings.Contains(string(raw), "Produce a verified result") || strings.Contains(string(raw), "No-stage execution") {
		t.Fatalf("observation leaked intent/title=%s", raw)
	}
}
