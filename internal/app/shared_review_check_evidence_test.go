package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestSharedReviewExistingEvidenceOriginalArtifact(t *testing.T) {
	h, _, i, task, _, _ := sharedReviewFixture(t)
	review := sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: task}))
	var page protocol.SharedDeliveryReviewPage
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		page, err = views.SharedDeliveryReviews(t.Context(), tx.Q(), i.IssueID, "pr", "", 10)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	var evidence []struct {
		ReviewID      string `json:"review_id"`
		Contributions []struct {
			TaskID            string `json:"task_id"`
			ArtifactID        string `json:"artifact_id"`
			CandidateRelation string `json:"candidate_relation"`
		} `json:"contributions"`
	}
	if err := json.Unmarshal(data["existing_evidence"], &evidence); err != nil {
		t.Fatalf("shared review must expose original contributor evidence before requesting another check: %v", err)
	}
	if len(evidence) != 1 || evidence[0].ReviewID != review.ReviewID || len(evidence[0].Contributions) != 1 {
		t.Fatalf("missing linked contribution evidence: %s", raw)
	}
	got := evidence[0].Contributions[0]
	if got.TaskID != task || got.ArtifactID != h.task(task).CurrentArtifactID || got.CandidateRelation != "head_matches" {
		t.Fatalf("original artifact/candidate provenance changed: %+v", got)
	}
}

func sharedReviewReportedContribution(t *testing.T) (*harness, protocol.Assignment, protocol.SharedDeliveryReview, string, string, protocol.ArtifactFile) {
	t.Helper()
	h, runtime, issue, source, _, _ := sharedReviewFixture(t)
	sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source})
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	spec := protocol.OwnerTaskSpec{Goal: "Provide original implementation check evidence", Acceptance: []string{"Evaluate existing checks before choosing additional independent validation"}, Delivery: "commit", Workspace: protocol.OwnerTaskWorkspace{Mounts: []protocol.OwnerTaskMount{{Repo: "tend"}}}, AgentSelection: &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.CreateIssueTaskPayload{Title: "Contributor checks", ExpectedAssignmentEpoch: issue.AssignmentEpoch, Spec: spec, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: true, Reason: "Preserve the independently reviewed contributor evidence fixture"}}, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var created taskResult
	if err := json.Unmarshal(op.Result, &created); err != nil {
		t.Fatal(err)
	}
	impl, ok := h.claimInputRun(runtime)
	if !ok || impl.TaskID != created.TaskID {
		t.Fatalf("implementation claim: %+v", impl)
	}
	if op := h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	file, _ := h.uploadResultFile(impl, "checks.log", []byte("original exact check output\n"))
	artifact := testArtifact(h.task(created.TaskID).WorkspaceName)
	artifact.Mounts[0].CommitSHA = strings.Repeat("a", 40)
	body := "Original check evidence\n" + strings.Repeat("command output\n", 18000)
	artifact.Report = &protocol.ResultReport{Body: body, Evidence: []string{"one targeted run; full suite not rerun"}, Uncertainties: []string{"one environment was unavailable"}}
	artifact.Files = []protocol.ArtifactFile{file}
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, artifact, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept, ok := h.claimInputRun(runtime)
	if !ok || accept.TaskID != created.TaskID || accept.Role != protocol.RoleAccept {
		t.Fatalf("acceptance claim: %+v", accept)
	}
	review := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: created.TaskID}))
	return h, accept, review, created.TaskID, body, file
}

func TestSharedReviewExistingEvidenceWorkerReadsOriginalOnDemand(t *testing.T) {
	h, accept, review, task, body, file := sharedReviewReportedContribution(t)
	context, err := h.app.ReadRunContext(t.Context(), runPrincipal(accept.RunID, accept.Role), protocol.IssueMaterialsQuery{Kind: "reviews", ID: "pr"})
	if err != nil {
		t.Fatal(err)
	}
	page := context.Materials.SharedReviews
	if page == nil || len(page.ExistingEvidence) != 1 || page.ExistingEvidence[0].ReviewID != review.ReviewID {
		t.Fatalf("missing worker review evidence: %+v", context)
	}
	var got protocol.SharedReviewContributionEvidence
	for _, item := range page.ExistingEvidence[0].Contributions {
		if item.TaskID == task {
			got = item
		}
	}
	digest := sha256.Sum256([]byte(body))
	if got.Report == nil || got.Report.BodyBytes != len(body) || got.Report.BodySHA256 != hex.EncodeToString(digest[:]) || got.Report.EvidenceCount != 1 || got.Report.UncertaintyCount != 1 || got.SpecVersion != 1 || len(got.Files) != 1 || got.Files[0] != file || got.CandidateRelation != "head_matches" {
		t.Fatalf("original report metadata was not preserved: %+v", got)
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 15000 || strings.Contains(string(raw), "command output") {
		t.Fatalf("index repeated a large original report: %d bytes", len(raw))
	}
	if got.ReadQuery == nil || got.ReadQuery.Kind != "artifacts" || got.ReadQuery.ID != got.ArtifactID {
		t.Fatal(got)
	}
	original, err := h.app.ReadRunContext(t.Context(), runPrincipal(accept.RunID, accept.Role), *got.ReadQuery)
	if err != nil {
		t.Fatal(err)
	}
	if len(original.Materials.Artifacts) != 1 {
		t.Fatal(original)
	}
	var payload protocol.ArtifactPayload
	if err := json.Unmarshal(original.Materials.Artifacts[0].Artifact.Ref, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Report == nil || payload.Report.Body != body || payload.Files[0] != file {
		t.Fatal("on-demand original changed")
	}
	metadata, err := h.app.ReadRunContextFile(t.Context(), runPrincipal(accept.RunID, accept.Role), file.FileID)
	if err != nil || metadata.ArtifactFile != file {
		t.Fatalf("original file is not discoverable/readable: %+v %v", metadata, err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var raw string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT body_json FROM shared_delivery_reviews WHERE review_id=?`, review.ReviewID).Scan(&raw); err != nil {
			return err
		}
		if strings.Contains(raw, "existing_evidence") || strings.Contains(raw, "body_sha256") {
			t.Fatal("read-only index became persisted review authority")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSharedReviewExistingEvidenceCandidateDriftKeepsOriginalHistory(t *testing.T) {
	h, accept, review, task, body, _ := sharedReviewReportedContribution(t)
	originalID := h.task(task).CurrentArtifactID
	var replacementID string
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		original, err := store.GetArtifact(t.Context(), tx.Q(), originalID)
		if err != nil {
			return err
		}
		var payload protocol.ArtifactPayload
		if err := json.Unmarshal([]byte(original.RefJSON), &payload); err != nil {
			return err
		}
		payload.Mounts[0].CommitSHA = strings.Repeat("c", 40)
		payload.Mounts[0].SnapshotSHA = strings.Repeat("d", 40)
		payload.Report.Body = "new candidate checks are not checks for the reviewed head"
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		replacementID = h.ids.New(protocol.PrefixArtifact)
		hash := sha256.Sum256(raw)
		digest := hex.EncodeToString(hash[:])
		if err := tx.InsertArtifact(t.Context(), replacementID, task, original.RunID, original.Kind, string(raw), digest, original.CreatedAt+1); err != nil {
			return err
		}
		row, err := store.GetTask(t.Context(), tx.Q(), task)
		if err != nil {
			return err
		}
		row.CurrentArtifactID = replacementID
		return tx.UpdateTask(t.Context(), row)
	}); err != nil {
		t.Fatal(err)
	}
	context, err := h.app.ReadRunContext(t.Context(), runPrincipal(accept.RunID, accept.Role), protocol.IssueMaterialsQuery{Kind: "reviews", ID: "pr"})
	if err != nil {
		t.Fatal(err)
	}
	var got protocol.SharedReviewContributionEvidence
	for _, item := range context.Materials.SharedReviews.ExistingEvidence[0].Contributions {
		if item.TaskID == task {
			got = item
		}
	}
	if got.ArtifactID != replacementID || got.CandidateRelation != "different_head" || got.HistoryQuery.Kind != "tasks" || got.HistoryQuery.ID != task {
		t.Fatalf("new candidate was mislabeled as original evidence: %+v / %s", got, review.HeadSHA)
	}
	history, err := h.app.ReadRunContext(t.Context(), runPrincipal(accept.RunID, accept.Role), *got.HistoryQuery)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Materials.Tasks) != 1 {
		t.Fatal(history)
	}
	found := false
	for _, item := range history.Materials.Tasks[0].Artifacts {
		if item.ArtifactID == originalID {
			var payload protocol.ArtifactPayload
			if err := json.Unmarshal(item.Ref, &payload); err != nil {
				t.Fatal(err)
			}
			found = payload.Report != nil && payload.Report.Body == body && payload.Mounts[0].CommitSHA == review.HeadSHA
		}
	}
	if !found {
		t.Fatal("immutable original candidate evidence disappeared from Task history")
	}
}

func TestSharedReviewExistingEvidenceBoundsIndexAndMarksMissing(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	review := protocol.SharedDeliveryReview{ReviewID: "review-index", HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40)}
	for range 21 {
		task := canonicalTaskReadFixture(t, h, issue)
		review.Links = append(review.Links, protocol.SharedDeliveryReviewLink{IssueID: issue.IssueID, DeliveryID: "pr", TaskID: task})
	}
	var evidence protocol.SharedReviewExistingEvidence
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		scope, err := views.MaterialScope(t.Context(), tx.Q(), []string{issue.IssueID})
		if err != nil {
			return err
		}
		evidence, err = views.SharedReviewExistingEvidence(t.Context(), tx.Q(), review, scope)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if evidence.ContributorCount != 21 || len(evidence.Contributions) != 20 || !evidence.ContributionsMore || len(review.Links) != 21 {
		t.Fatalf("truncated contributions were hidden: %+v", evidence)
	}
	for _, item := range evidence.Contributions {
		if item.ArtifactID != "" || item.Report != nil || item.ReadQuery != nil || item.CandidateRelation != "not_provided" || item.HistoryQuery.ID != item.TaskID {
			t.Fatalf("absent evidence was invented: %+v", item)
		}
	}
}

func TestSharedReviewExistingEvidenceLinkedPRDoesNotExposeIsolatedOriginal(t *testing.T) {
	h, _, review, task, _, file := sharedReviewReportedContribution(t)
	other := createCanonicalOwnerTestIssue(t, h)
	other.Plan.Deliveries = []protocol.IssueDelivery{{DeliveryID: "pr", Provider: review.Provider, Repository: review.Repository, Number: review.Number}}
	other = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, other, "update", protocol.UpdateIssuePayload{Plan: other.Plan, ExpectedAssignmentEpoch: other.AssignmentEpoch}))
	sharedReviewAction(t, h, other.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr"})
	var origin string
	for _, link := range review.Links {
		if link.TaskID == task {
			origin = link.IssueID
		}
	}
	materialProjectForTest(t, h, origin, "isolated")
	context, err := h.app.ReadIssueMaterials(t.Context(), other.IssueID, protocol.IssueMaterialsQuery{Kind: "reviews", ID: "pr"})
	if err != nil {
		t.Fatal(err)
	}
	if context.SharedReviews == nil || len(context.SharedReviews.Items) != 1 || context.SharedReviews.Items[0].ReviewID != review.ReviewID {
		t.Fatal("shared PR result was lost", context)
	}
	found := false
	for _, item := range context.SharedReviews.ExistingEvidence[0].Contributions {
		if item.TaskID == task {
			found = true
			if item.Availability != "outside_context" || item.ArtifactID != "" || item.Report != nil || len(item.Files) != 0 || len(item.Mounts) != 0 || item.ReadQuery != nil || item.HistoryQuery != nil {
				t.Fatalf("PR link exposed an isolated original: %+v", item)
			}
		}
	}
	if !found {
		t.Fatal("outside-context provenance silently disappeared")
	}
	if _, err := h.app.ReadIssueMaterials(t.Context(), other.IssueID, protocol.IssueMaterialsQuery{Kind: "artifacts", ID: h.task(task).CurrentArtifactID}); protocol.CodeOf(err) != protocol.CodeNotFound {
		t.Fatal("isolated artifact became readable", err)
	}
	if _, err := h.app.ReadIssueMaterialFile(t.Context(), other.IssueID, file.FileID); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("isolated file became readable", err)
	}
}

func TestSharedReviewExistingEvidenceWorkerReadsSameProjectContributor(t *testing.T) {
	h, _, review, sourceTask, body, file := sharedReviewReportedContribution(t)
	var sourceIssue string
	for _, link := range review.Links {
		if link.TaskID == sourceTask {
			sourceIssue = link.IssueID
		}
	}
	other := createCanonicalOwnerTestIssue(t, h)
	other.Plan.Deliveries = []protocol.IssueDelivery{{DeliveryID: "pr", Provider: review.Provider, Repository: review.Repository, Number: review.Number}}
	other = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, other, "update", protocol.UpdateIssuePayload{Plan: other.Plan, ExpectedAssignmentEpoch: other.AssignmentEpoch}))
	sharedReviewAction(t, h, other.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr"})
	other = canonicalTaskIssue(t, h, other.IssueID)
	spec := protocol.OwnerTaskSpec{Goal: "Assess existing same-project PR checks", Acceptance: []string{"Read the contributor's original report before selecting additional checks"}, Delivery: "commit", Workspace: protocol.OwnerTaskWorkspace{Mounts: []protocol.OwnerTaskMount{{Repo: "tend"}}}, AgentSelection: &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}}}
	createdOp := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: other.IssueID}, protocol.CreateIssueTaskPayload{Title: "Assess linked evidence", ExpectedAssignmentEpoch: other.AssignmentEpoch, Spec: spec}, rev(other.Revision)), CanonicalOwnerTaskCreate{})
	var created taskResult
	if err := json.Unmarshal(createdOp.Result, &created); err != nil || createdOp.Status != protocol.OpSucceeded {
		t.Fatal(createdOp, err)
	}
	var runtime protocol.RunnerRuntime
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		row, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
		runtime = row.Runtime
		return err
	}); err != nil {
		t.Fatal(err)
	}
	worker, ok := h.claimInputRun(&runtime)
	if !ok || worker.TaskID != created.TaskID {
		t.Fatalf("ordinary worker did not claim current Issue task: %+v", worker)
	}
	principal := runPrincipal(worker.RunID, worker.Role)
	find := func(page *protocol.SharedDeliveryReviewPage) protocol.SharedReviewContributionEvidence {
		t.Helper()
		if page == nil {
			t.Fatal("missing shared review page")
		}
		for _, group := range page.ExistingEvidence {
			for _, item := range group.Contributions {
				if item.TaskID == sourceTask {
					return item
				}
			}
		}
		t.Fatal("linked contributor absent")
		return protocol.SharedReviewContributionEvidence{}
	}
	var direct protocol.SharedDeliveryReviewPage
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		direct, err = views.SharedDeliveryReviews(t.Context(), tx.Q(), other.IssueID, "pr", "", 10)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ownerMaterials, err := h.app.ReadIssueMaterials(t.Context(), other.IssueID, protocol.IssueMaterialsQuery{Kind: "reviews", ID: "pr"})
	if err != nil {
		t.Fatal(err)
	}
	workerContext, err := h.app.ReadRunContext(t.Context(), principal, protocol.IssueMaterialsQuery{Kind: "reviews", ID: "pr"})
	if err != nil {
		t.Fatal(err)
	}
	originalID := h.task(sourceTask).CurrentArtifactID
	for entry, page := range map[string]*protocol.SharedDeliveryReviewPage{"direct": &direct, "owner materials": ownerMaterials.SharedReviews, "ordinary worker": workerContext.Materials.SharedReviews} {
		item := find(page)
		if item.Availability != "available" || item.CandidateRelation != "head_matches" || item.ArtifactID != originalID || item.ReadQuery == nil {
			t.Fatalf("%s disagrees about same-project contributor: %+v", entry, item)
		}
	}
	verifyOriginal := func(item protocol.SharedReviewContributionEvidence) {
		t.Helper()
		if item.ReadQuery == nil {
			t.Fatal("no original reference")
		}
		original, err := h.app.ReadRunContext(t.Context(), principal, *item.ReadQuery)
		if err != nil || len(original.Materials.Artifacts) != 1 {
			t.Fatalf("original query is not usable by the actual worker: %+v %v", item.ReadQuery, err)
		}
		var payload protocol.ArtifactPayload
		if err := json.Unmarshal(original.Materials.Artifacts[0].Artifact.Ref, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Report == nil || payload.Report.Body != body || len(payload.Files) != 1 || payload.Files[0] != file {
			t.Fatal("original report/file metadata changed")
		}
		metadata, err := h.app.ReadRunContextFile(t.Context(), principal, file.FileID)
		if err != nil || metadata.ArtifactFile != file {
			t.Fatalf("original file metadata unavailable: %+v %v", metadata, err)
		}
	}
	verifyOriginal(find(workerContext.Materials.SharedReviews))
	materialProjectForTest(t, h, sourceIssue, "isolated-contributor")
	isolated, err := h.app.ReadRunContext(t.Context(), principal, protocol.IssueMaterialsQuery{Kind: "reviews", ID: "pr"})
	if err != nil {
		t.Fatal(err)
	}
	item := find(isolated.Materials.SharedReviews)
	if item.Availability != "outside_context" || item.ArtifactID != "" || item.Report != nil || len(item.Files) != 0 || item.ReadQuery != nil {
		t.Fatal("shared PR widened cross-project original access", item)
	}
	if _, err := h.app.ReadRunContext(t.Context(), principal, protocol.IssueMaterialsQuery{IssueID: sourceIssue, Kind: "artifacts", ID: originalID}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("cross-project original query accepted", err)
	}
	var original store.ArtifactRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		original, err = store.GetArtifact(t.Context(), tx.Q(), originalID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	other = canonicalTaskIssue(t, h, other.IssueID)
	grant := protocol.GrantArtifactSharePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: other.AssignmentEpoch, Reason: "Share this exact original contributor report"}, ArtifactID: originalID, RefSHA256: store.ArtifactRefSHA256(original.RefJSON)}
	granted := h.exec(owner, h.env(protocol.OpGrantArtifactShare, protocol.Target{Type: "issue", ID: other.IssueID}, grant, rev(other.Revision)), ArtifactShareCommand{})
	if granted.Status != protocol.OpSucceeded {
		t.Fatal(granted)
	}
	var share protocol.ArtifactShare
	if err := json.Unmarshal(granted.Result, &share); err != nil {
		t.Fatal(err)
	}
	shared, err := h.app.ReadRunContext(t.Context(), principal, protocol.IssueMaterialsQuery{Kind: "reviews", ID: "pr"})
	if err != nil {
		t.Fatal(err)
	}
	sharedItem := find(shared.Materials.SharedReviews)
	if sharedItem.Availability != "available" || sharedItem.ReadQuery == nil || sharedItem.ReadQuery.IssueID != other.IssueID {
		t.Fatal("exact existing share did not preserve reader context", sharedItem)
	}
	verifyOriginal(sharedItem)
	// Restoring ordinary same-project knowledge must not erase the reader's
	// existing explicit revocation of this exact artifact.
	materialProjectForTest(t, h, sourceIssue, other.Plan.Project)
	other = canonicalTaskIssue(t, h, other.IssueID)
	revoke := protocol.RevokeArtifactSharePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: other.AssignmentEpoch, Reason: "Withdraw this exact contributor artifact"}, ShareID: share.ShareID, ExpectedShareRevision: share.Revision}
	if op := h.exec(owner, h.env(protocol.OpRevokeArtifactShare, protocol.Target{Type: "issue", ID: other.IssueID}, revoke, rev(other.Revision)), ArtifactShareCommand{Revoke: true}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	revoked, err := h.app.ReadRunContext(t.Context(), principal, protocol.IssueMaterialsQuery{Kind: "reviews", ID: "pr"})
	if err != nil {
		t.Fatal(err)
	}
	revokedItem := find(revoked.Materials.SharedReviews)
	if revokedItem.Availability != "outside_context" || revokedItem.ReadQuery != nil || revokedItem.Report != nil || len(revokedItem.Files) != 0 {
		t.Fatal("same-project expansion bypassed reader-specific revocation", revokedItem)
	}
	if _, err := h.app.ReadRunContext(t.Context(), principal, protocol.IssueMaterialsQuery{IssueID: sourceIssue, Kind: "artifacts", ID: originalID}); protocol.CodeOf(err) != protocol.CodeNotFound {
		t.Fatal("original lookup lost reader-specific revocation", err)
	}
}
