package app

import (
	"encoding/json"
	"io"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// Project edits are a coherent store fixture, not a second configured project
// or an assignment workflow. Tasks keep their original execution settings.
func materialProjectForTest(t *testing.T, h *harness, issue, project string) {
	t.Helper()
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO projects(project,title,weight,repos_json,retired,created_at,updated_at) VALUES(?, 'Material boundary',1,'[]',1,0,0) ON CONFLICT DO NOTHING`, project); err != nil {
			return err
		}
		next, err := store.GetCanonicalIssue(t.Context(), tx.Q(), issue)
		if err != nil {
			return err
		}
		expected := next.Revision
		next.Revision++
		next.Plan.Project = project
		return tx.SaveCanonicalIssue(t.Context(), next, expected, store.IssueRevisionProvenance{Actor: owner, RequestID: h.ids.New(protocol.PrefixRequest), Reason: "Explicit project boundary fixture"})
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIssueAutonomySourceChangesDoNotRequireTaskApplicabilityPermission(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	task, original := agreementFixture(t, h, issue)
	appendAgreementSource(t, h, issue.IssueID)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		candidates := []scheduler.Candidate{{TaskID: task, SpecVersion: 2, Kind: protocol.ActionImplement, DependenciesDone: true}}
		if err := views.ApplyIssueDependencies(t.Context(), tx.Q(), candidates); err != nil {
			return err
		}
		if candidates[0].ContextPending || !candidates[0].DependenciesDone {
			t.Fatal("ordinary source changes became a new execution permission", candidates[0])
		}
		raw, err := store.GetSpec(t.Context(), tx.Q(), task, 2)
		if err == nil && raw != original {
			t.Fatal("source observation rewrote the frozen Task")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIssueAutonomyMaterialsAreReadableAcrossSameProjectWithoutNewShare(t *testing.T) {
	h := newDeliverableHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	related := createCanonicalOwnerTestIssue(t, h)
	file := uploadChatFile(t, h, "related-evidence.txt", "text/plain", []byte("retained project knowledge"))
	materialMessage(t, h, related.IssueID, file.FileID)
	page, err := h.app.ReadScopedIssueMaterials(t.Context(), issue.IssueID, related.IssueID, protocol.IssueMaterialsQuery{Kind: "files", ID: file.FileID})
	if err != nil || len(page.Files) != 1 || page.Files[0].SHA256 != file.SHA256 {
		t.Fatal("same-project materials require another share", page, err)
	}
	metadata, err := h.app.ReadIssueMaterialFile(t.Context(), issue.IssueID, file.FileID)
	if err != nil || metadata.SHA256 != file.SHA256 {
		t.Fatal("project discovery and exact file reads disagree", metadata, err)
	}
	if h.count("artifact_shares") != 0 {
		t.Fatal("ordinary project reading created an authorization record")
	}
}

func TestIssueAutonomyDefinitionChangeDoesNotDiscardRunningResult(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, issue)
	impl, ok, _ := h.claim("linux-1")
	if !ok || impl.TaskID != task {
		t.Fatal(impl)
	}
	if op := h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	before := h.task(task)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	issue.Plan.Intent = "The owner will assess this clarification while the current work continues"
	op := canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Plan: issue.Plan})
	if op.Status != protocol.OpSucceeded {
		t.Fatal("ordinary definition edit required a Task disposition", op)
	}
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, testArtifact(before.WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	current := h.task(task)
	if current.ControlMode != protocol.ControlManaged || current.SpecVersion != before.SpecVersion || current.Lifecycle != protocol.TaskAwaitingAcceptance || current.CurrentArtifactID == "" {
		t.Fatal("a context digest became a result fence", current)
	}
	accept, ok, _ := h.claim("linux-1")
	if !ok || accept.TaskID != task || accept.Role != protocol.RoleAccept {
		t.Fatal("normal acceptance required a separate context permission", accept)
	}
}

func TestIssueAutonomySharedKnowledgeRetainsHandoffAndExplicitIsolation(t *testing.T) {
	h := newDeliverableHarness(t)
	source := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, source)
	impl, ok, _ := h.claim("linux-1")
	if !ok || impl.TaskID != task {
		t.Fatal(impl)
	}
	file, _ := h.uploadResultFile(impl, "result.txt", []byte("shared original"))
	draft, _ := h.uploadResultFile(impl, "draft.txt", []byte("private upload"))
	result := testArtifact(h.task(task).WorkspaceName)
	result.Files = []protocol.ArtifactFile{file}
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, result, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	target := createCanonicalOwnerTestIssue(t, h)
	other := createCanonicalOwnerTestIssue(t, h)
	isolated := createCanonicalOwnerTestIssue(t, h)
	materialProjectForTest(t, h, isolated.IssueID, "isolated-project")
	read := func(issue string, allowed bool) {
		t.Helper()
		metadata, body, err := h.app.OpenIssueMaterialFile(t.Context(), issue, file.FileID)
		if !allowed {
			if protocol.CodeOf(err) != protocol.CodeForbidden {
				if body != nil {
					body.Close()
				}
				t.Fatal("isolated material was readable", metadata, err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		bytes, err := io.ReadAll(body)
		body.Close()
		if err != nil || string(bytes) != "shared original" || metadata.SHA256 != file.SHA256 {
			t.Fatal(metadata, err)
		}
	}
	read(target.IssueID, true)
	read(isolated.IssueID, false)
	if _, err := h.app.ReadIssueMaterialFile(t.Context(), target.IssueID, draft.FileID); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("unsubmitted upload became shared knowledge", err)
	}
	artifactID := h.task(task).CurrentArtifactID
	var artifact store.ArtifactRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		artifact, err = store.GetArtifact(t.Context(), tx.Q(), artifactID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	grant := func(issue string) protocol.ArtifactShare {
		t.Helper()
		i := canonicalTaskIssue(t, h, issue)
		op := h.exec(owner, h.env(protocol.OpGrantArtifactShare, protocol.Target{Type: "issue", ID: issue}, protocol.GrantArtifactSharePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "Share this exact retained original"}, ArtifactID: artifactID, RefSHA256: store.ArtifactRefSHA256(artifact.RefJSON)}, rev(i.Revision)), ArtifactShareCommand{})
		var share protocol.ArtifactShare
		if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &share) != nil {
			t.Fatal(op)
		}
		return share
	}
	shared := grant(isolated.IssueID)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		i, err := store.GetCanonicalIssue(t.Context(), tx.Q(), isolated.IssueID)
		if err != nil {
			return err
		}
		expected := i.Revision
		i.Revision++
		i.AssignmentEpoch++
		return tx.SaveCanonicalIssue(t.Context(), i, expected, store.IssueRevisionProvenance{Actor: owner, RequestID: h.ids.New(protocol.PrefixRequest), Reason: "Handoff preserves knowledge"})
	}); err != nil {
		t.Fatal(err)
	}
	read(isolated.IssueID, true)
	// A deliberate target-specific revoke also overrides the project default,
	// without hiding the same artifact from another project peer.
	revocable := grant(target.IssueID)
	for _, pair := range []struct {
		issue string
		share protocol.ArtifactShare
	}{{target.IssueID, revocable}, {isolated.IssueID, shared}} {
		i := canonicalTaskIssue(t, h, pair.issue)
		op := h.exec(owner, h.env(protocol.OpRevokeArtifactShare, protocol.Target{Type: "issue", ID: pair.issue}, protocol.RevokeArtifactSharePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "Explicitly revoke this target reference"}, ShareID: pair.share.ShareID, ExpectedShareRevision: pair.share.Revision}, rev(i.Revision)), ArtifactShareCommand{Revoke: true})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		read(pair.issue, false)
		if pair.issue == target.IssueID {
			page, err := h.app.ReadScopedIssueMaterials(t.Context(), pair.issue, source.IssueID, protocol.IssueMaterialsQuery{Kind: "tasks", ID: task})
			if err != nil || len(page.Tasks) != 1 || len(page.Tasks[0].Artifacts) != 0 {
				t.Fatal("revoked artifact body leaked through the Task projection", page, err)
			}
		}
	}
	read(other.IssueID, true)
	materialProjectForTest(t, h, target.IssueID, "")
	materialProjectForTest(t, h, other.IssueID, "")
	if _, err := h.app.ReadScopedIssueMaterials(t.Context(), target.IssueID, other.IssueID, protocol.IssueMaterialsQuery{}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("unrelated unnamed projects were unioned", err)
	}
}
