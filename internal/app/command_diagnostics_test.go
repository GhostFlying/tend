package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func diagnosticFacts(t *testing.T, op protocol.Operation, code protocol.Code) map[string]any {
	t.Helper()
	if op.Status != protocol.OpFailed || op.Error == nil || op.Error.Code != code {
		t.Fatal(op)
	}
	var facts map[string]any
	if err := json.Unmarshal(op.Error.Details, &facts); err != nil {
		t.Fatal("missing recovery facts", op.Error, err)
	}
	return facts
}

func TestIssueProgressDiagnosticsPreserveCASAndUnicodeBoundary(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	target := protocol.Target{Type: "issue", ID: issue.IssueID}
	p := protocol.UpdateIssueProgressPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Summary: strings.Repeat("界", 501), NextStep: "continue"}
	bad := h.exec(owner, h.env("issues.update_progress", target, p, rev(issue.Revision)), UpdateIssueProgress{})
	facts := diagnosticFacts(t, bad, protocol.CodeInvalidRequest)
	if facts["field"] != "summary" || facts["max"] != float64(500) || facts["actual"] != float64(501) || h.count("issue_progress_revisions") != 0 || canonicalTaskIssue(t, h, issue.IssueID).Revision != issue.Revision {
		t.Fatal("invalid progress wrote facts or lost character limit", facts)
	}
	p.Summary = strings.Repeat("界", 500)
	p.NextStep = strings.Repeat("🙂", 240)
	if op := h.exec(owner, h.env("issues.update_progress", target, p, rev(issue.Revision)), UpdateIssueProgress{}); op.Status != protocol.OpSucceeded {
		t.Fatal("valid Unicode boundary rejected", op)
	}
	stale := h.exec(owner, h.env("issues.update_progress", target, p, rev(issue.Revision)), UpdateIssueProgress{})
	facts = diagnosticFacts(t, stale, protocol.CodeStaleRevision)
	if facts["issue_id"] != issue.IssueID || facts["current_control_revision"] != float64(issue.Revision+1) || facts["current_assignment_epoch"] != float64(issue.AssignmentEpoch) || facts["recovery_tool"] != "issues_read" || h.count("issue_progress_revisions") != 1 {
		t.Fatal("stale progress mutated or lost original identity", facts)
	}
	p.Summary, p.NextStep = "", ""
	if op := h.exec(owner, h.env("issues.update_progress", target, p, rev(issue.Revision+1)), UpdateIssueProgress{}); op.Status != protocol.OpSucceeded {
		t.Fatal("clear progress rejected", op)
	}
}

func TestMissingControlRevisionNamesOriginalTarget(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	p := protocol.UpdateIssueProgressPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Summary: "progress", NextStep: "continue"}
	op := h.exec(owner, h.env("issues.update_progress", protocol.Target{Type: "issue", ID: issue.IssueID}, p, nil), UpdateIssueProgress{})
	facts := diagnosticFacts(t, op, protocol.CodeInvalidRequest)
	if facts["field"] != "expected_control_revision" || facts["target_id"] != issue.IssueID || facts["recovery_tool"] != "issues_read" || h.count("issue_progress_revisions") != 0 {
		t.Fatal("missing revision was silently refreshed", facts)
	}
}

func TestEndedRunMessageDiagnosticsPreserveOriginalResult(t *testing.T) {
	h, as := messageRun(t)
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(as.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	before := h.task(as.TaskID)
	op := h.exec(owner, sendMessageEnv(h, as, "continue after completion"), SendRunMessage{})
	facts := diagnosticFacts(t, op, protocol.CodeInvalidTransition)
	if facts["run_id"] != as.RunID || facts["task_id"] != as.TaskID || facts["current_control_revision"] != float64(before.ControlRevision) || facts["recovery_tool"] != "tasks_read" || h.count("run_messages") != 0 || h.task(as.TaskID).CurrentArtifactID != before.CurrentArtifactID {
		t.Fatal("ended run was supplemented or lost recovery identity", facts)
	}
}

func TestRunMessageMissingRevisionPointsToExistingTaskRead(t *testing.T) {
	h, as := messageRun(t)
	env := sendMessageEnv(h, as, "supplement with missing revision")
	env.ExpectedControlRevision = nil
	op := h.exec(owner, env, SendRunMessage{})
	facts := diagnosticFacts(t, op, protocol.CodeInvalidRequest)
	if facts["task_id"] != as.TaskID || facts["run_id"] != as.RunID || facts["recovery_tool"] != "tasks_read" || h.count("run_messages") != 0 {
		t.Fatal("missing revision lacks an existing original Task recovery entry", facts)
	}
}

func TestCompletedTaskSpecDiagnosticsRetainImmutableAgreement(t *testing.T) {
	h := newDeliverableHarness(t)
	created := h.createReportTask("persistent investigation", temporaryReportSpec())
	as, ok, _ := h.claimDeliverableRun()
	if !ok {
		t.Fatal("claim")
	}
	h.reportDirectory(as, t.TempDir())
	h.completeReport(as)
	before := h.task(created.TaskID)
	changed := temporaryReportSpec()
	changed.Goal = "Continue the original investigation"
	op := h.exec(owner, h.env("tasks.update_intent", taskTarget(created.TaskID), updateIntentPayload{Spec: &changed}, rev(before.ControlRevision)), UpdateIntent{})
	facts := diagnosticFacts(t, op, protocol.CodeInvalidTransition)
	if facts["task_id"] != created.TaskID || facts["current_control_revision"] != float64(before.ControlRevision) || facts["continuation_tool"] != "tasks_continue" || h.task(created.TaskID).SpecVersion != before.SpecVersion || h.task(created.TaskID).CurrentArtifactID != before.CurrentArtifactID {
		t.Fatal("completed agreement rewritten or recovery missing", facts)
	}
}
