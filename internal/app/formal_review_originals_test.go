package app

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestFormalReviewReadsSameIssueOwnerOriginalAndContinuesTask(t *testing.T) {
	h, runtime, issue, producer, _ := reviewManagerAssignment(t)
	enableOwnerResultReviewer(t, h, runtime)
	upload := func(name, body string) protocol.ArtifactFile {
		t.Helper()
		op, err := h.app.UploadDeliverableFile(t.Context(), runPrincipal(producer.RunID, producer.Role), h.ids.New(protocol.PrefixRequest), name, strings.NewReader(body))
		var file protocol.ArtifactFile
		if err != nil || op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &file) != nil {
			t.Fatal(op, err)
		}
		return file
	}
	file := upload("review-continuation.md", "Read the current candidate and original check evidence; retain earlier results.")
	private := upload("private-rollout.jsonl", "Private execution session, not a formal result")
	validation := upload("validation.log", "Original validation for the submitted candidate")
	if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{Action: "submit_result", TaskID: producer.TaskID, RunID: producer.RunID, Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Actual implementation and original validation"}, Files: []protocol.ArtifactFile{validation}}, IssueControlPayload: protocol.IssueControlPayload{Reason: "Submit actual owner implementation and validation"}}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	implementation := h.task(producer.TaskID).CurrentArtifactID
	if _, err := h.app.ReadIssueMaterialFile(t.Context(), issue.IssueID, file.FileID); err != nil {
		t.Fatal("owner lost its own uploaded original", err)
	}
	foreign := createCanonicalOwnerTestIssue(t, h)
	foreign.Plan.Background = append(foreign.Plan.Background, protocol.SharedFact{Kind: protocol.SharedFactInput, Text: "A foreign reference cannot publish a private upload", Source: file.FileID})
	canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, foreign, "update", protocol.UpdateIssuePayload{ExpectedAssignmentEpoch: foreign.AssignmentEpoch, Plan: foreign.Plan}))
	if _, err := h.app.ReadIssueMaterialFile(t.Context(), foreign.IssueID, file.FileID); err == nil {
		t.Fatal("another Issue published a private owner upload")
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	issue.Plan.Background = append(issue.Plan.Background, protocol.SharedFact{Kind: protocol.SharedFactInput, Text: "Formal continuation original", Source: file.FileID})
	issue.Plan.Background = append(issue.Plan.Background, protocol.SharedFact{Kind: protocol.SharedFactInput, Text: "A mention alone is not a publication: " + private.FileID, Source: "private session remains excluded"})
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Plan: issue.Plan}))
	task := createSharedReviewerTaskForTest(t, h, issue.IssueID, protocol.AgentRequirement{CategoryID: "implementer", AgentID: "linux-1/beta"})
	reviewer, ok := h.claimInputRun(runtime)
	if !ok || reviewer.TaskID != task || reviewer.Execution.Identity == producer.Execution.Identity {
		t.Fatal("no actual independent reviewer", reviewer)
	}
	read := func(as protocol.Assignment) {
		t.Helper()
		p := runPrincipal(as.RunID, as.Role)
		page, err := h.app.ReadRunContext(t.Context(), p, protocol.IssueMaterialsQuery{Kind: "files", ID: file.FileID})
		if err != nil || len(page.Materials.Files) != 1 || page.Materials.Files[0].ArtifactFile != file {
			t.Fatal("formal owner original missing from reviewer context", page, err)
		}
		listed, err := h.app.ReadRunContext(t.Context(), p, protocol.IssueMaterialsQuery{Kind: "files"})
		if err != nil || len(listed.Materials.Files) != 2 {
			t.Fatal("a private text reference poisoned the formal file index", listed, err)
		}
		metadata, body, err := h.app.OpenRunContextFile(t.Context(), p, file.FileID)
		if err != nil {
			t.Fatal(err)
		}
		bytes, err := io.ReadAll(body)
		body.Close()
		if err != nil || string(bytes) != "Read the current candidate and original check evidence; retain earlier results." || metadata.ArtifactFile != file {
			t.Fatal("original bytes or identity changed", metadata, err)
		}
		if _, err := h.app.ReadRunContextFile(t.Context(), p, private.FileID); err == nil {
			t.Fatal("unsubmitted private rollout became readable", err)
		}
		if metadata, err := h.app.ReadRunContextFile(t.Context(), p, validation.FileID); err != nil || metadata.ArtifactFile != validation {
			t.Fatal("formal submitted validation is unavailable", metadata, err)
		}
		artifact, err := h.app.ReadRunContext(t.Context(), p, protocol.IssueMaterialsQuery{Kind: "artifacts", ID: implementation})
		if err != nil || len(artifact.Materials.Artifacts) != 1 {
			t.Fatal("actual implementation report is unavailable", artifact, err)
		}
	}
	read(reviewer)
	if op := h.runAs(reviewer, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(reviewer, protocol.OpRunsReportWorkspace, protocol.WorkspaceReport{BackendID: reviewer.Workspace.BackendID, Path: t.TempDir(), Baseline: json.RawMessage(`{"files":[]}`)}, ReportWorkspace{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	result := protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Read the actual formal continuation original; further candidate checks remain."}}
	if op := h.runAs(reviewer, protocol.OpRunsSubmitArtifact, result, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	original := h.task(task).CurrentArtifactID
	releaseSharedReviewerForTest(t, h, task)
	if op := continueTaskForTest(t, h, task, "Continue review using the same original and retained report"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next, ok := h.claimInputRun(runtime)
	if !ok || next.TaskID != task || next.RunID == reviewer.RunID || next.SpecVersion != reviewer.SpecVersion || *next.Execution != *reviewer.Execution {
		t.Fatal("not the original reviewer Task continuation", next)
	}
	read(next)
	page, err := h.app.ReadRunContext(t.Context(), runPrincipal(next.RunID, next.Role), protocol.IssueMaterialsQuery{Kind: "artifacts", ID: original})
	if err != nil || len(page.Materials.Artifacts) != 1 {
		t.Fatal("continuation lost its original report", page, err)
	}
	if op := h.runAs(next, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(next, protocol.OpRunsReportWorkspace, protocol.WorkspaceReport{BackendID: next.Workspace.BackendID, Path: h.resourceForTask(task).Path, Baseline: json.RawMessage(`{"files":[]}`)}, ReportWorkspace{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	result.Report = &protocol.ResultReport{Body: "Continued the original review using formal implementation, validation and continuation originals", ReviewedArtifactID: implementation, Verdict: protocol.VerdictPassed}
	if op := h.runAs(next, protocol.OpRunsSubmitArtifact, result, SubmitArtifact{}); op.Status != protocol.OpSucceeded || h.task(task).CurrentArtifactID == original {
		t.Fatal("same Task did not retain a new actual report", op)
	}
}

func TestFormalReferenceDoesNotPublishImplementationPrivateUpload(t *testing.T) {
	h := newDeliverableHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, issue)
	impl, ok, _ := h.claim("linux-1")
	if !ok || impl.TaskID != task {
		t.Fatal(impl)
	}
	private, _ := h.uploadResultFile(impl, "private-trace.jsonl", []byte("Unsubmitted implementation execution"))
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	issue.Plan.Background = append(issue.Plan.Background, protocol.SharedFact{Kind: protocol.SharedFactInput, Text: "A context reference cannot publish implementation internals", Source: private.FileID})
	canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Plan: issue.Plan}))
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	reviewer, ok, _ := h.claim("linux-1")
	if !ok || reviewer.TaskID != task || reviewer.Role != protocol.RoleAccept {
		t.Fatal(reviewer)
	}
	if _, err := h.app.ReadRunContextFile(t.Context(), runPrincipal(reviewer.RunID, reviewer.Role), private.FileID); err == nil {
		t.Fatal("formal context reference exposed a private implementation trace")
	}
}
