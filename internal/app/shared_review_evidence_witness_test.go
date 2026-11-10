package app

import (
	"encoding/json"
	"io"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestSharedReviewExistingEvidenceGrantContextWitness(t *testing.T) {
	for _, relation := range []string{"linked-sibling", "ancestor"} {
		t.Run(relation, func(t *testing.T) {
			h, _, review, sourceTask, body, file := sharedReviewReportedContribution(t)
			var sourceIssue string
			for _, link := range review.Links {
				if link.TaskID == sourceTask {
					sourceIssue = link.IssueID
				}
			}
			materialProjectForTest(t, h, sourceIssue, "isolated-original")
			recipient := createCanonicalOwnerTestIssue(t, h)
			recipient.Plan.Deliveries = []protocol.IssueDelivery{{DeliveryID: "pr", Provider: review.Provider, Repository: review.Repository, Number: review.Number}}
			recipient = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, recipient, "update", protocol.UpdateIssuePayload{Plan: recipient.Plan, ExpectedAssignmentEpoch: recipient.AssignmentEpoch}))
			sharedReviewAction(t, h, recipient.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr"})
			create := canonicalOwnerIssueCreate{Plan: protocol.IssuePlan{Title: "Assess retained contributor evidence", Project: "default", Intent: "Read original evidence under the existing context", Stages: []protocol.IssueStage{}, Deliveries: recipient.Plan.Deliveries}}
			if relation == "ancestor" {
				create.ParentIssueID = recipient.IssueID
			}
			op := h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, create, nil), CanonicalOwnerIssueCommand{Action: "create"})
			var reader protocol.Issue
			if err := json.Unmarshal(op.Result, &reader); err != nil || op.Status != protocol.OpSucceeded {
				t.Fatal(op, err)
			}
			sharedReviewAction(t, h, reader.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr"})
			reader = canonicalTaskIssue(t, h, reader.IssueID)
			reader = admitExistingIssueFixture(t, h, reader)
			spec := protocol.OwnerTaskSpec{Goal: "Assess linked PR evidence", Acceptance: []string{"Read original contributor checks"}, Delivery: "commit", Workspace: protocol.OwnerTaskWorkspace{Mounts: []protocol.OwnerTaskMount{{Repo: "tend"}}}, AgentSelection: &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}}}
			op = h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: reader.IssueID}, protocol.CreateIssueTaskPayload{Title: "Assess linked evidence", ExpectedAssignmentEpoch: reader.AssignmentEpoch, Spec: spec}, rev(reader.Revision)), CanonicalOwnerTaskCreate{})
			var created taskResult
			if err := json.Unmarshal(op.Result, &created); err != nil || op.Status != protocol.OpSucceeded {
				t.Fatal(op, err)
			}
			var runtime protocol.RunnerRuntime
			var original store.ArtifactRow
			originalID := h.task(sourceTask).CurrentArtifactID
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				row, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
				if err != nil {
					return err
				}
				runtime = row.Runtime
				original, err = store.GetArtifact(t.Context(), tx.Q(), originalID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			worker, ok := h.claimInputRun(&runtime)
			if !ok || worker.TaskID != created.TaskID {
				t.Fatal(worker)
			}
			principal := runPrincipal(worker.RunID, worker.Role)
			grant := func(issue string) protocol.ArtifactShare {
				t.Helper()
				current := canonicalTaskIssue(t, h, issue)
				payload := protocol.GrantArtifactSharePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: current.AssignmentEpoch, Reason: "Share this exact immutable contributor evidence"}, ArtifactID: original.ArtifactID, RefSHA256: store.ArtifactRefSHA256(original.RefJSON)}
				op := h.exec(owner, h.env(protocol.OpGrantArtifactShare, protocol.Target{Type: "issue", ID: issue}, payload, rev(current.Revision)), ArtifactShareCommand{})
				var result protocol.ArtifactShare
				if err := json.Unmarshal(op.Result, &result); err != nil || op.Status != protocol.OpSucceeded {
					t.Fatal(op, err)
				}
				return result
			}
			grant(recipient.IssueID)
			find := func() protocol.SharedReviewContributionEvidence {
				t.Helper()
				context, err := h.app.ReadRunContext(t.Context(), principal, protocol.IssueMaterialsQuery{Kind: "reviews", ID: "pr"})
				if err != nil {
					t.Fatal(err)
				}
				for _, group := range context.Materials.SharedReviews.ExistingEvidence {
					for _, item := range group.Contributions {
						if item.TaskID == sourceTask {
							return item
						}
					}
				}
				t.Fatal("linked original absent")
				return protocol.SharedReviewContributionEvidence{}
			}
			item := find()
			if item.Availability != "available" || item.ReadQuery == nil {
				t.Fatal("existing granting context was not discovered", item)
			}
			originalContext, err := h.app.ReadRunContext(t.Context(), principal, *item.ReadQuery)
			if err != nil || len(originalContext.Materials.Artifacts) != 1 {
				t.Fatalf("advertised worker original is unreadable: %+v %v", item.ReadQuery, err)
			}
			if relation == "linked-sibling" && item.ReadQuery.IssueID != recipient.IssueID {
				t.Fatalf("reference did not select the actual granting sibling: %+v", item.ReadQuery)
			}
			var payload protocol.ArtifactPayload
			if err := json.Unmarshal(originalContext.Materials.Artifacts[0].Artifact.Ref, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Report == nil || payload.Report.Body != body {
				t.Fatal("original report changed")
			}
			ownerContext, err := h.app.ReadScopedIssueMaterials(t.Context(), reader.IssueID, item.ReadQuery.IssueID, *item.ReadQuery)
			if err != nil || len(ownerContext.Artifacts) != 1 {
				t.Fatalf("advertised owner original is unreadable: %+v %v", item.ReadQuery, err)
			}
			metadata, stream, err := h.app.OpenRunContextFile(t.Context(), principal, file.FileID)
			if err != nil {
				t.Fatal("original file bytes unavailable", err)
			}
			bytes, readErr := io.ReadAll(stream)
			closeErr := stream.Close()
			if readErr != nil || closeErr != nil || metadata.ArtifactFile != file || string(bytes) != "original exact check output\n" {
				t.Fatal("original file changed", readErr, closeErr)
			}
			if relation == "linked-sibling" {
				materialProjectForTest(t, h, recipient.IssueID, "isolated-recipient")
				if got := find(); got.Availability != "outside_context" || got.ReadQuery != nil || got.Report != nil {
					t.Fatal("cross-project recipient granted access", got)
				}
				materialProjectForTest(t, h, recipient.IssueID, "default")
				rootShare := grant(reader.IssueID)
				reader = canonicalTaskIssue(t, h, reader.IssueID)
				revoke := protocol.RevokeArtifactSharePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: reader.AssignmentEpoch, Reason: "Withdraw this exact original from the reader"}, ShareID: rootShare.ShareID, ExpectedShareRevision: rootShare.Revision}
				op := h.exec(owner, h.env(protocol.OpRevokeArtifactShare, protocol.Target{Type: "issue", ID: reader.IssueID}, revoke, rev(reader.Revision)), ArtifactShareCommand{Revoke: true})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				if got := find(); got.Availability != "outside_context" || got.ReadQuery != nil || got.Report != nil {
					t.Fatal("another recipient bypassed reader-specific revocation", got)
				}
				if _, err := h.app.ReadRunContext(t.Context(), principal, protocol.IssueMaterialsQuery{IssueID: recipient.IssueID, Kind: "artifacts", ID: original.ArtifactID}); protocol.CodeOf(err) != protocol.CodeNotFound {
					t.Fatal("reader revocation lost while selecting sibling", err)
				}
			}
		})
	}
}
