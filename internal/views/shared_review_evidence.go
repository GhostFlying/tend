package views

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// SharedReviewExistingEvidence indexes contributor artifacts without treating
// free-form check claims as successful commands or persisting derived evidence.
func SharedReviewExistingEvidence(ctx context.Context, q store.Querier, review protocol.SharedDeliveryReview, scope store.MaterialScope) (protocol.SharedReviewExistingEvidence, error) {
	out := protocol.SharedReviewExistingEvidence{ReviewID: review.ReviewID, HeadSHA: review.HeadSHA, BaseSHA: review.BaseSHA, Contributions: []protocol.SharedReviewContributionEvidence{}}
	links := map[string]protocol.SharedDeliveryReviewLink{}
	for _, link := range review.Links {
		if link.TaskID != "" {
			if _, ok := links[link.TaskID]; !ok {
				links[link.TaskID] = link
			}
		}
	}
	tasks := make([]string, 0, len(links))
	for task := range links {
		tasks = append(tasks, task)
	}
	sort.Strings(tasks)
	out.ContributorCount = len(tasks)
	if len(tasks) > 20 {
		out.ContributionsMore = true
		tasks = tasks[:20]
	}
	for _, id := range tasks {
		link := links[id]
		item := protocol.SharedReviewContributionEvidence{
			IssueID: link.IssueID, TaskID: id, Availability: "not_provided", CandidateRelation: "not_provided",
			MissingStructuredFields: []string{"check_commands", "check_exit_codes", "check_environment", "skipped_checks", "uncovered_scope"},
		}
		history, err := sharedReviewOriginalQuery(ctx, q, scope, "tasks", id, link.IssueID)
		if err != nil {
			return out, err
		}
		item.HistoryQuery = history
		task, err := store.GetTask(ctx, q, id)
		if err != nil {
			return out, err
		}
		if task.CurrentArtifactID == "" {
			if history == nil {
				item.Availability = "outside_context"
			}
			item.MissingStructuredFields = append(item.MissingStructuredFields, "artifact")
			out.Contributions = append(out.Contributions, item)
			continue
		}
		original, err := sharedReviewOriginalQuery(ctx, q, scope, "artifacts", task.CurrentArtifactID, link.IssueID)
		if err != nil {
			return out, err
		}
		if original == nil {
			item.Availability = "outside_context"
			out.Contributions = append(out.Contributions, item)
			continue
		}
		item.Availability = "available"
		artifact, err := store.GetArtifact(ctx, q, task.CurrentArtifactID)
		if err != nil {
			return out, err
		}
		item.ArtifactID, item.RunID, item.RefSHA256, item.CreatedAt = artifact.ArtifactID, artifact.RunID, artifact.Digest, artifact.CreatedAt
		if err := q.QueryRowContext(ctx, `SELECT a.target_spec_version FROM runs r JOIN plan_actions a ON a.action_id=r.action_id WHERE r.run_id=? AND r.task_id=?`, artifact.RunID, id).Scan(&item.SpecVersion); err != nil {
			return out, fmt.Errorf("views: contributor artifact execution: %w", err)
		}
		var payload protocol.ArtifactPayload
		if err := json.Unmarshal([]byte(artifact.RefJSON), &payload); err != nil {
			return out, fmt.Errorf("views: contributor artifact: %w", err)
		}
		for _, mount := range payload.Mounts {
			head := mount.SnapshotSHA
			if head == "" {
				head = mount.CommitSHA
			}
			if head == "" || review.HeadSHA == "" {
				continue
			}
			if item.CandidateRelation == "not_provided" {
				item.CandidateRelation = "different_head"
			}
			if head == review.HeadSHA {
				item.CandidateRelation = "head_matches"
				break
			}
		}
		item.Mounts = payload.Mounts
		if len(item.Mounts) > 10 {
			item.MountsMore = true
			item.Mounts = item.Mounts[:10]
		}
		item.Files = payload.Files
		if len(item.Files) > 10 {
			item.FilesMore = true
			item.Files = item.Files[:10]
		}
		if payload.Report != nil {
			hash := sha256.Sum256([]byte(payload.Report.Body))
			item.Report = &protocol.SharedReviewReportReference{BodyBytes: len(payload.Report.Body), BodySHA256: hex.EncodeToString(hash[:]), EvidenceCount: len(payload.Report.Evidence), UncertaintyCount: len(payload.Report.Uncertainties)}
		} else {
			item.MissingStructuredFields = append(item.MissingStructuredFields, "report")
		}
		item.ReadQuery = original
		out.Contributions = append(out.Contributions, item)
	}
	return out, nil
}

// sharedReviewOriginalQuery returns a context selector only after the ordinary
// read resolver proves that exact original is visible under that selector.
func sharedReviewOriginalQuery(ctx context.Context, q store.Querier, scope store.MaterialScope, kind, id, sourceIssue string) (*protocol.IssueMaterialsQuery, error) {
	candidates := []string{}
	if len(scope.IssueIDs) > 0 {
		candidates = append(candidates, scope.IssueIDs[0])
	}
	candidates = append(candidates, sourceIssue)
	if kind == "artifacts" {
		recipients, err := store.FollowupIDs(ctx, q, `SELECT DISTINCT issue_id FROM artifact_shares WHERE artifact_id=? AND status='active' ORDER BY issue_id`, id)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, recipients...)
	}
	seen := map[string]bool{}
	for _, issue := range candidates {
		if issue == "" || seen[issue] {
			continue
		}
		seen[issue] = true
		selected, err := SelectMaterialIssue(ctx, q, scope, issue)
		if err != nil {
			if protocol.CodeOf(err) == protocol.CodeForbidden || protocol.CodeOf(err) == protocol.CodeNotFound || errors.Is(err, store.ErrNotFound) {
				continue
			}
			return nil, err
		}
		ids, _, _, err := store.MaterialIDs(ctx, q, selected, kind, id, "", 1)
		if err != nil {
			return nil, err
		}
		if len(ids) > 0 {
			return &protocol.IssueMaterialsQuery{IssueID: issue, Kind: kind, ID: id}, nil
		}
	}
	return nil, nil
}
