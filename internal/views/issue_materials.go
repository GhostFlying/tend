package views

import (
	"context"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// MaterialScope resolves the caller's project and explicit context references.
// Assignment epochs and native sessions do not erase retained knowledge.
func MaterialScope(ctx context.Context, q store.Querier, issueIDs []string) (store.MaterialScope, error) {
	scope := store.MaterialScope{IssueIDs: []string{}}
	if len(issueIDs) > 0 {
		scope.ReaderIssueID = issueIDs[0]
	}
	for _, id := range issueIDs {
		if slices.Contains(scope.IssueIDs, id) {
			continue
		}
		issue, err := store.GetIssue(ctx, q, id)
		if err != nil {
			return scope, err
		}
		scope.IssueIDs = append(scope.IssueIDs, id)
		if id == scope.ReaderIssueID {
			scope.Project = issue.Project
		}
	}
	return scope, nil
}

func IssueMaterialFile(ctx context.Context, q store.Querier, scope store.MaterialScope, id string) (protocol.IssueMaterialFile, error) {
	out := protocol.IssueMaterialFile{Sources: []protocol.IssueMaterialSource{}}
	if !protocol.ValidID(protocol.PrefixFile, id) {
		return out, protocol.Errorf(protocol.CodeInvalidRequest, "exact file_id required")
	}
	// Exact originals can be resolved directly in the same project; this does
	// not load every project's Issue definition or retained Task context.
	var err error
	scope, err = store.ExactMaterialFileScope(ctx, q, scope, id)
	if err != nil {
		return out, err
	}
	out.SourcesMore = scope.SourcesMore
	found, _, _, err := store.MaterialIDs(ctx, q, scope, "files", id, "", 1)
	if err != nil {
		return out, err
	}
	if len(found) == 0 {
		return out, protocol.Errorf(protocol.CodeForbidden, "file is outside this Issue context")
	}
	attachment, err := store.GetConversationAttachment(ctx, q, id)
	if err == nil {
		out.ArtifactFile = protocol.ArtifactFile{FileID: id, Name: attachment.Name, Size: attachment.Size, SHA256: attachment.SHA256}
		out.MediaType = attachment.MediaType
		out.Sources, err = store.MaterialMessageSource(ctx, q, scope, id)
		if len(out.Sources) > 100 {
			out.Sources = out.Sources[:100]
			out.SourcesMore = true
		}
		if err != nil {
			return out, err
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return out, err
	} else {
		file, artifact, err := store.GetFinalizedDeliverableFile(ctx, q, id)
		if err == nil {
			out.ArtifactFile = file.ArtifactFile
			out.Sources = append(out.Sources, protocol.IssueMaterialSource{Kind: "artifact", TaskID: file.TaskID, ArtifactID: artifact, Status: "retained"})
		} else if errors.Is(err, store.ErrNotFound) {
			file, acceptance, err := store.GetFinalizedAcceptanceFile(ctx, q, id)
			if err != nil {
				return out, err
			}
			out.ArtifactFile = file.ArtifactFile
			out.Sources = append(out.Sources, protocol.IssueMaterialSource{Kind: "acceptance", TaskID: file.TaskID, AcceptanceID: acceptance.AcceptanceID, Status: string(acceptance.Verdict)})
		} else {
			return out, err
		}
	}
	refs, err := store.MaterialFileReferences(ctx, q, scope, id)
	if err != nil {
		return out, err
	}
	if len(refs) > 100 {
		refs = refs[:100]
		out.SourcesMore = true
	}
	for _, ref := range refs {
		if !slices.Contains(out.Sources, ref) {
			out.Sources = append(out.Sources, ref)
		}
	}
	if len(out.Sources) > 100 {
		out.Sources = out.Sources[:100]
		out.SourcesMore = true
	}
	return out, nil
}

func IssueMaterials(ctx context.Context, q store.Querier, scope store.MaterialScope, query protocol.IssueMaterialsQuery) (protocol.IssueMaterials, error) {
	out := protocol.IssueMaterials{Kind: query.Kind, IssueIDs: scope.IssueIDs}
	if out.Kind == "" {
		out.Kind = "overview"
	}
	if query.Limit == 0 {
		query.Limit = 50
	}
	if query.Limit < 1 || query.Limit > 100 {
		return out, protocol.Errorf(protocol.CodeInvalidRequest, "limit must be between 1 and 100")
	}
	var err error
	out.Seq, err = store.LatestEventSeq(ctx, q)
	if err != nil {
		return out, err
	}
	if out.Kind == "overview" || out.Kind == "issues" {
		if out.Kind == "overview" && query.ID != "" {
			return out, protocol.Errorf(protocol.CodeInvalidRequest, "overview does not take object selectors")
		}
		ids, more, next, err := store.MaterialIssuePage(ctx, q, scope, out.Kind == "issues", query.ID, query.After, query.Limit)
		if err != nil {
			return out, err
		}
		out.More, out.Next = more, next
		if query.ID != "" && len(ids) == 0 {
			return out, protocol.Errorf(protocol.CodeNotFound, "Issue is outside this context")
		}
		for _, id := range ids {
			canonical, err := ownerCanonicalIdentity(ctx, q, id)
			if err != nil {
				return out, err
			}
			if !canonical {
				item, err := store.GetIssue(ctx, q, id)
				if err != nil {
					return out, err
				}
				out.LegacyContexts = append(out.LegacyContexts, item)
				continue
			}
			var item protocol.IssueView
			if out.Kind == "issues" {
				item.Issue, err = store.GetCanonicalIssue(ctx, q, id)
			} else {
				item, err = CanonicalIssue(ctx, q, id, 0)
			}
			if err != nil {
				return out, err
			}
			out.Issues = append(out.Issues, item)
		}
		return out, nil
	}
	if out.Kind == "deliveries" {
		if query.ID != "" || query.After != "" {
			return out, protocol.Errorf(protocol.CodeInvalidRequest, "delivery context does not take object selectors")
		}
		for _, id := range scope.IssueIDs {
			canonical, err := ownerCanonicalIdentity(ctx, q, id)
			if err != nil {
				return out, err
			}
			if !canonical {
				continue
			}
			page, err := CanonicalIssueDeliveries(ctx, q, id)
			if err != nil {
				return out, err
			}
			out.Deliveries = append(out.Deliveries, page)
		}
		return out, nil
	}
	if out.Kind == "deployment" {
		if query.ID != "" || query.After != "" {
			return out, protocol.Errorf(protocol.CodeInvalidRequest, "deployment context does not take object selectors")
		}
		return out, nil
	}
	if out.Kind == "files" && query.ID != "" {
		scope, err = store.ExactMaterialFileScope(ctx, q, scope, query.ID)
		if err != nil {
			return out, err
		}
	}
	ids, more, next, err := store.MaterialIDs(ctx, q, scope, out.Kind, query.ID, query.After, query.Limit)
	if err != nil {
		return out, err
	}
	out.More, out.Next = more, next
	if query.ID != "" && len(ids) == 0 {
		return out, protocol.Errorf(protocol.CodeNotFound, "object is not in this Issue context")
	}
	for _, id := range ids {
		switch out.Kind {
		case "sources", "messages":
			item, err := OwnerIssueMessage(ctx, q, id)
			if err != nil {
				return out, err
			}
			if out.Kind == "sources" {
				out.Sources = append(out.Sources, item)
			} else {
				out.Messages = append(out.Messages, item)
			}
		case "facts":
			var item protocol.IssueMaterialFact
			err := q.QueryRowContext(ctx, `SELECT fact_id,COALESCE(work_item_id,''),scope,project,kind,body,source_message_id,attribution,COALESCE(supersedes,''),needs_review,superseded,created_at FROM work_item_context_facts WHERE fact_id=?`, id).Scan(&item.ID, &item.IssueID, &item.Scope, &item.Project, &item.Kind, &item.Body, &item.SourceMessageID, &item.Attribution, &item.Supersedes, &item.NeedsReview, &item.Superseded, &item.CreatedAt)
			if err != nil {
				return out, err
			}
			out.Facts = append(out.Facts, item)
		case "tasks":
			item, err := Task(ctx, q, id)
			if err != nil {
				return out, err
			}
			denied, err := store.MaterialDeniedTaskArtifacts(ctx, q, scope, id)
			if err != nil {
				return out, err
			}
			item.Artifacts = slices.DeleteFunc(item.Artifacts, func(a protocol.ArtifactView) bool { return slices.Contains(denied, a.ArtifactID) })
			out.Tasks = append(out.Tasks, item)
		case "artifacts":
			item, err := Artifact(ctx, q, id)
			if err != nil {
				return out, err
			}
			out.Artifacts = append(out.Artifacts, item)
		case "questions":
			item, err := store.GetDecision(ctx, q, id)
			if err != nil {
				return out, err
			}
			out.Questions = append(out.Questions, Decision(item))
		case "acceptances":
			var item store.AcceptanceRow
			err := q.QueryRowContext(ctx, `SELECT acceptance_id,task_id,run_id,spec_version,artifact_id,verdict,issues_json,evidence_json,files_json,created_at FROM acceptances WHERE acceptance_id=?`, id).Scan(&item.AcceptanceID, &item.TaskID, &item.RunID, &item.SpecVersion, &item.ArtifactID, &item.Verdict, &item.IssuesJSON, &item.EvidenceJSON, &item.FilesJSON, &item.CreatedAt)
			if err != nil {
				return out, err
			}
			views := Acceptances([]store.AcceptanceRow{item}, true)
			out.Acceptances = append(out.Acceptances, protocol.IssueMaterialAcceptance{AcceptanceView: views[0], TaskID: item.TaskID, RunID: item.RunID, CreatedAt: item.CreatedAt})
		case "files":
			item, err := IssueMaterialFile(ctx, q, scope, id)
			if err != nil {
				return out, err
			}
			out.Files = append(out.Files, item)
		}
	}
	return out, nil
}
