package views

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
		readIssue := scope.ReaderIssueID
		if len(scope.IssueIDs) > 0 {
			readIssue = scope.IssueIDs[0]
		}
		taskIDs, _, _, err := store.MaterialIDs(ctx, q, scope, "tasks", id, "", 1)
		if err != nil {
			return out, err
		}
		if len(taskIDs) > 0 {
			item.HistoryQuery = &protocol.IssueMaterialsQuery{IssueID: readIssue, Kind: "tasks", ID: id}
		}
		task, err := store.GetTask(ctx, q, id)
		if err != nil {
			return out, err
		}
		if task.CurrentArtifactID == "" {
			if len(taskIDs) == 0 {
				item.Availability = "outside_context"
			}
			item.MissingStructuredFields = append(item.MissingStructuredFields, "artifact")
			out.Contributions = append(out.Contributions, item)
			continue
		}
		artifactIDs, _, _, err := store.MaterialIDs(ctx, q, scope, "artifacts", task.CurrentArtifactID, "", 1)
		if err != nil {
			return out, err
		}
		if len(artifactIDs) == 0 {
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
		item.ReadQuery = &protocol.IssueMaterialsQuery{IssueID: readIssue, Kind: "artifacts", ID: artifact.ArtifactID}
		out.Contributions = append(out.Contributions, item)
	}
	return out, nil
}
