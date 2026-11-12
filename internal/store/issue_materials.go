package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
)

// MaterialScope is derived from actual Issue relationships, never caller paths.
type MaterialScope struct {
	IssueIDs      []string
	ReaderIssueID string
	Project       string
	ExactFileID   string
	SourcesMore   bool
	// UploadTaskID is the authenticated Run's Task, independent of selected context.
	// Empty keeps owner reads within Tasks directly owned by ReaderIssueID.
	UploadTaskID string
}

// The same scoped candidates back listing and exact metadata/byte reads.
const materialScopeSQL = `WITH scope(id) AS (SELECT value FROM json_each(?1,'$.issue_ids')
 UNION SELECT work_item_id FROM work_items WHERE ?2<>'' AND json_extract(?1,'$.project')<>'' AND project=json_extract(?1,'$.project')),
 denied_artifacts(id) AS (
 SELECT DISTINCT s.artifact_id FROM artifact_shares s WHERE s.issue_id=json_extract(?1,'$.reader_issue_id') AND s.status='revoked'
 AND NOT EXISTS(SELECT 1 FROM artifact_shares active WHERE active.issue_id=s.issue_id AND active.artifact_id=s.artifact_id AND active.status='active')),
 tasks_scope(id) AS (
 SELECT task_id FROM task_work_item_owners WHERE work_item_id IN (SELECT id FROM scope)
	UNION SELECT task_id FROM issue_manager_tasks WHERE issue_id IN (SELECT id FROM scope)
 UNION SELECT l.task_id FROM work_item_task_links l WHERE l.work_item_id IN (SELECT id FROM scope) AND NOT EXISTS(SELECT 1 FROM issue_controls c WHERE c.issue_id=l.work_item_id)),
 messages_scope(id) AS (
 SELECT message_id FROM message_work_items WHERE work_item_id IN (SELECT id FROM scope) AND active=1
 UNION SELECT m.message_id FROM agent_conversations c JOIN conversation_messages m USING(conversation_id) WHERE c.work_item_id IN (SELECT id FROM scope)
 UNION SELECT message_id FROM message_context_obligations WHERE work_item_id IN (SELECT id FROM scope)
 UNION SELECT source_message_id FROM issue_control_revisions WHERE issue_id IN (SELECT id FROM scope)
 UNION SELECT source_message_id FROM work_item_context_facts WHERE work_item_id IN (SELECT id FROM scope)
 UNION SELECT s.message_id FROM background_batches b JOIN background_sources s USING(batch_id) WHERE b.work_item_id IN (SELECT id FROM scope)),
 artifact_candidates(id) AS (
 SELECT artifact_id FROM artifacts WHERE task_id IN (SELECT id FROM tasks_scope)
 UNION SELECT a.artifact_id FROM artifacts a JOIN work_item_task_links l ON l.task_id=a.task_id WHERE l.work_item_id IN (SELECT id FROM scope)
 UNION SELECT s.artifact_id FROM artifact_shares s JOIN artifacts a ON a.artifact_id=s.artifact_id AND a.ref_json=s.ref_json WHERE s.issue_id IN (SELECT id FROM scope) AND s.status='active'),
 artifacts_scope(id) AS (SELECT id FROM artifact_candidates WHERE id NOT IN (SELECT id FROM denied_artifacts)),
 known_files(id) AS (SELECT file_id FROM conversation_attachments WHERE ?2='' OR file_id=?2
 UNION SELECT file_id FROM deliverable_files WHERE ?2='' OR file_id=?2),
 background_refs(id,issue_id,task_id,spec_version,locator,kind) AS (
 SELECT k.id,w.work_item_id,'',0,COALESCE(json_extract(f.value,'$.source'),''),'background'
 FROM known_files k JOIN work_items w ON w.work_item_id IN (SELECT id FROM scope) AND instr(w.body_json,k.id)>0,
 json_each(w.body_json,'$.background') f
 WHERE instr(COALESCE(json_extract(f.value,'$.source'),'')||' '||COALESCE(json_extract(f.value,'$.text'),''),k.id)>0
 UNION SELECT k.id,COALESCE(json_extract(c.value,'$.work_item_id'),''),s.task_id,s.spec_version,COALESCE(json_extract(f.value,'$.source'),''),'task_context'
 FROM known_files k JOIN task_specs s ON s.task_id IN (SELECT id FROM tasks_scope) AND instr(s.body_json,k.id)>0,
 json_each(s.body_json,'$.shared_context') c,json_each(c.value,'$.facts') f
 WHERE instr(COALESCE(json_extract(f.value,'$.source'),'')||' '||COALESCE(json_extract(f.value,'$.text'),''),k.id)>0),
 uploads_scope(id) AS (
 SELECT f.file_id FROM deliverable_files f WHERE f.task_id IN (SELECT id FROM tasks_scope)
 AND (f.task_id=json_extract(?1,'$.upload_task_id') OR (json_extract(?1,'$.upload_task_id')='' AND
 (EXISTS(SELECT 1 FROM task_work_item_owners o WHERE o.task_id=f.task_id AND o.work_item_id=json_extract(?1,'$.reader_issue_id'))
 OR EXISTS(SELECT 1 FROM issue_manager_tasks o WHERE o.task_id=f.task_id AND o.issue_id=json_extract(?1,'$.reader_issue_id')))))
 AND (?2='' OR f.file_id=?2)),
 files_scope(id) AS (
 SELECT a.file_id FROM conversation_message_attachments a WHERE a.message_id IN (SELECT id FROM messages_scope)
 UNION SELECT json_extract(f.value,'$.file_id') FROM artifacts a,json_each(a.ref_json,'$.files') f WHERE a.artifact_id IN (SELECT id FROM artifacts_scope)
 UNION SELECT json_extract(f.value,'$.file_id') FROM acceptances a,json_each(a.files_json) f WHERE (a.task_id IN (SELECT id FROM tasks_scope) OR a.artifact_id IN (SELECT id FROM artifacts_scope))
 UNION SELECT json_extract(f.value,'$.file_id') FROM task_specs s,json_each(s.body_json,'$.inputs') f WHERE s.task_id IN (SELECT id FROM tasks_scope) AND (?2='' OR instr(s.body_json,?2)>0)
 UNION SELECT id FROM background_refs
 UNION SELECT id FROM uploads_scope) `

func materialArgs(scope MaterialScope) (string, string, error) {
	ids, err := json.Marshal(struct {
		Issues     []string `json:"issue_ids"`
		Reader     string   `json:"reader_issue_id"`
		Project    string   `json:"project"`
		UploadTask string   `json:"upload_task_id"`
	}{scope.IssueIDs, scope.ReaderIssueID, scope.Project, scope.UploadTaskID})
	if err != nil {
		return "", "", err
	}
	return string(ids), scope.ExactFileID, nil
}

// ExactMaterialFileScope first locates an original through its saved message
// or output identity. Only a historical context-only original needs the
// fallback query over matching retained facts. Neither path loads project
// Issue definitions or unrelated Task context JSON into the application.
func ExactMaterialFileScope(ctx context.Context, q Querier, scope MaterialScope, file string) (MaterialScope, error) {
	scope.ExactFileID = file
	ids, _, err := materialArgs(scope)
	if err != nil {
		return scope, err
	}
	const candidates = `WITH scope(id) AS (SELECT value FROM json_each(?1,'$.issue_ids') UNION SELECT work_item_id FROM work_items WHERE project<>'' AND project=json_extract(?1,'$.project')) `
	found, err := FollowupIDs(ctx, q, candidates+`SELECT id FROM scope WHERE
 EXISTS(SELECT 1 FROM conversation_message_attachments f JOIN message_work_items m USING(message_id) WHERE f.file_id=?2 AND m.work_item_id=scope.id AND m.active=1)
 OR EXISTS(SELECT 1 FROM conversation_message_attachments f JOIN conversation_messages m USING(message_id) JOIN agent_conversations c USING(conversation_id) WHERE f.file_id=?2 AND c.work_item_id=scope.id)
 OR EXISTS(SELECT 1 FROM conversation_message_attachments f JOIN message_context_obligations m USING(message_id) WHERE f.file_id=?2 AND m.work_item_id=scope.id)
 OR EXISTS(SELECT 1 FROM conversation_message_attachments f JOIN issue_control_revisions r ON r.source_message_id=f.message_id WHERE f.file_id=?2 AND r.issue_id=scope.id)
 OR EXISTS(SELECT 1 FROM conversation_message_attachments f JOIN work_item_context_facts r ON r.source_message_id=f.message_id WHERE f.file_id=?2 AND r.work_item_id=scope.id)
 OR EXISTS(SELECT 1 FROM conversation_message_attachments f JOIN background_sources s USING(message_id) JOIN background_batches b USING(batch_id) WHERE f.file_id=?2 AND b.work_item_id=scope.id)
 OR EXISTS(SELECT 1 FROM deliverable_files f JOIN task_work_item_owners o USING(task_id) WHERE f.file_id=?2 AND o.work_item_id=scope.id)
	OR EXISTS(SELECT 1 FROM deliverable_files f JOIN issue_manager_tasks o USING(task_id) WHERE f.file_id=?2 AND o.issue_id=scope.id)
 OR EXISTS(SELECT 1 FROM deliverable_files f JOIN work_item_task_links o USING(task_id) WHERE f.file_id=?2 AND o.work_item_id=scope.id)
 OR EXISTS(SELECT 1 FROM deliverable_files f JOIN artifacts a ON a.task_id=f.task_id JOIN artifact_shares s ON s.artifact_id=a.artifact_id AND s.ref_json=a.ref_json WHERE f.file_id=?2 AND s.issue_id=scope.id AND s.status='active')
 ORDER BY id LIMIT 101`, ids, file)
	if err != nil {
		return scope, err
	}
	if len(found) == 0 {
		found, err = FollowupIDs(ctx, q, candidates+`SELECT id FROM scope WHERE
 EXISTS(SELECT 1 FROM work_items w,json_each(w.body_json,'$.background') f WHERE w.work_item_id=scope.id AND instr(w.body_json,?2)>0 AND instr(COALESCE(json_extract(f.value,'$.source'),'')||' '||COALESCE(json_extract(f.value,'$.text'),''),?2)>0)
 OR EXISTS(SELECT 1 FROM task_specs s WHERE (EXISTS(SELECT 1 FROM task_work_item_owners o WHERE o.task_id=s.task_id AND o.work_item_id=scope.id) OR EXISTS(SELECT 1 FROM work_item_task_links l WHERE l.task_id=s.task_id AND l.work_item_id=scope.id AND NOT EXISTS(SELECT 1 FROM issue_controls c WHERE c.issue_id=l.work_item_id))) AND instr(s.body_json,?2)>0 AND
 (EXISTS(SELECT 1 FROM json_each(s.body_json,'$.inputs') f WHERE json_extract(f.value,'$.file_id')=?2)
 OR EXISTS(SELECT 1 FROM json_each(s.body_json,'$.shared_context') c,json_each(c.value,'$.facts') f WHERE instr(COALESCE(json_extract(f.value,'$.source'),'')||' '||COALESCE(json_extract(f.value,'$.text'),''),?2)>0))) ORDER BY id LIMIT 101`, ids, file)
		if err != nil {
			return scope, err
		}
	}
	if len(found) > 100 {
		scope.SourcesMore = true
		found = found[:100]
	}
	// Keep independently saved inputs in the reader's own context and explicit
	// target revocations, while avoiding all-project context expansion.
	for _, id := range found {
		present := false
		for _, existing := range scope.IssueIDs {
			present = present || existing == id
		}
		if !present {
			scope.IssueIDs = append(scope.IssueIDs, id)
		}
	}
	scope.Project = ""
	return scope, nil
}

// MaterialIssuePage discovers project knowledge without loading Issue bodies
// or Task histories outside the requested page. Empty projects are not global.
func MaterialIssuePage(ctx context.Context, q Querier, scope MaterialScope, project bool, id, after string, limit int) ([]string, bool, string, error) {
	ids, _, err := materialArgs(scope)
	if err != nil {
		return nil, false, "", err
	}
	out, err := FollowupIDs(ctx, q, `SELECT work_item_id FROM work_items WHERE (work_item_id IN (SELECT value FROM json_each(?1,'$.issue_ids')) OR (?2 AND project<>'' AND project=json_extract(?1,'$.project'))) AND work_item_id>?3 AND (?4='' OR work_item_id=?4) ORDER BY work_item_id LIMIT ?5`, ids, project, after, id, limit+1)
	if err != nil {
		return nil, false, "", err
	}
	more, next := len(out) > limit, ""
	if more {
		out = out[:limit]
		next = out[len(out)-1]
	}
	return out, more, next, nil
}

// MaterialDeniedTaskArtifacts applies the same explicit target exception to
// artifact bodies nested in a public Task projection.
func MaterialDeniedTaskArtifacts(ctx context.Context, q Querier, scope MaterialScope, task string) ([]string, error) {
	ids, files, err := materialArgs(scope)
	if err != nil {
		return nil, err
	}
	return FollowupIDs(ctx, q, materialScopeSQL+`SELECT artifact_id FROM artifacts WHERE task_id=? AND artifact_id IN (SELECT id FROM denied_artifacts)`, ids, files, task)
}

func MaterialIDs(ctx context.Context, q Querier, scope MaterialScope, kind, id, after string, limit int) ([]string, bool, string, error) {
	ids, files, err := materialArgs(scope)
	if err != nil {
		return nil, false, "", err
	}
	var query string
	args := []any{ids, files}
	numeric := false
	switch kind {
	case "files":
		query = `SELECT id FROM files_scope WHERE (EXISTS(SELECT 1 FROM conversation_attachments a WHERE a.file_id=files_scope.id) OR EXISTS(SELECT 1 FROM deliverable_files f WHERE f.file_id=files_scope.id)) AND id>? AND (?='' OR id=?) ORDER BY id LIMIT ?`
	case "tasks":
		query = `SELECT id FROM tasks_scope WHERE id>? AND (?='' OR id=?) ORDER BY id LIMIT ?`
	case "artifacts":
		query = `SELECT id FROM artifacts_scope WHERE id>? AND (?='' OR id=?) ORDER BY id LIMIT ?`
	case "acceptances":
		query = `SELECT acceptance_id FROM acceptances WHERE (task_id IN (SELECT id FROM tasks_scope) OR artifact_id IN (SELECT id FROM artifacts_scope)) AND acceptance_id>? AND (?='' OR acceptance_id=?) ORDER BY acceptance_id LIMIT ?`
	case "questions":
		query = `SELECT decision_id FROM decisions WHERE task_id IN (SELECT id FROM tasks_scope) AND decision_id>? AND (?='' OR decision_id=?) ORDER BY decision_id LIMIT ?`
	case "facts":
		query = `SELECT fact_id FROM work_item_context_facts WHERE work_item_id IN (SELECT id FROM scope) AND fact_id>? AND (?='' OR fact_id=?) ORDER BY fact_id LIMIT ?`
	case "sources", "messages":
		numeric = true
		cursor := int64(0)
		if after != "" {
			cursor, err = strconv.ParseInt(after, 10, 64)
			if err != nil || cursor < 0 {
				return nil, false, "", protocol.Errorf(protocol.CodeInvalidRequest, "invalid message cursor")
			}
		}
		query = `SELECT m.message_id FROM conversation_messages m WHERE m.message_id IN (SELECT id FROM messages_scope) AND m.seq>? AND (?='' OR m.message_id=?) ORDER BY m.seq LIMIT ?`
		args = append(args, cursor, id, id, limit+1)
	default:
		return nil, false, "", protocol.Errorf(protocol.CodeInvalidRequest, "invalid material kind")
	}
	if !numeric {
		args = append(args, after, id, id, limit+1)
	}
	out, err := FollowupIDs(ctx, q, materialScopeSQL+query, args...)
	if err != nil {
		return nil, false, "", fmt.Errorf("store: Issue material IDs: %w", err)
	}
	more := len(out) > limit
	next := ""
	if more {
		out = out[:limit]
		next = out[len(out)-1]
		if numeric {
			var seq int64
			if err = q.QueryRowContext(ctx, `SELECT seq FROM conversation_messages WHERE message_id=?`, next).Scan(&seq); err != nil {
				return nil, false, "", err
			}
			next = strconv.FormatInt(seq, 10)
		}
	}
	return out, more, next, nil
}

// MaterialMessageSource preserves the real owner/share version and withdrawal.
func MaterialMessageSource(ctx context.Context, q Querier, scope MaterialScope, fileID string) ([]protocol.IssueMaterialSource, error) {
	ids, files, err := materialArgs(scope)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, materialScopeSQL+`SELECT m.message_id,COALESCE(d.content_revision,1),COALESCE(c.withdrawn_at,0),COALESCE((SELECT work_item_id FROM message_work_items w WHERE w.message_id=m.message_id AND w.work_item_id IN (SELECT id FROM scope) AND active=1 ORDER BY work_item_id LIMIT 1),(SELECT work_item_id FROM agent_conversations WHERE conversation_id=m.conversation_id),'') FROM conversation_messages m JOIN conversation_message_attachments a USING(message_id) LEFT JOIN message_dispositions d USING(message_id) LEFT JOIN conversation_message_controls c USING(message_id) WHERE a.file_id=? AND m.message_id IN (SELECT id FROM messages_scope) ORDER BY m.seq LIMIT 101`, ids, files, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.IssueMaterialSource{}
	for rows.Next() {
		var source protocol.IssueMaterialSource
		var withdrawn int64
		source.Kind = "message"
		if err = rows.Scan(&source.MessageID, &source.ContentRevision, &withdrawn, &source.IssueID); err != nil {
			return nil, err
		}
		source.Status = "retained"
		if withdrawn != 0 {
			source.Status = "withdrawn"
		}
		out = append(out, source)
	}
	return out, rows.Err()
}

// MaterialUploadFile reads a confirmed upload in the caller's own Task or
// owner Issue. Context locators alone never publish an unfinished foreign file.
func MaterialUploadFile(ctx context.Context, q Querier, scope MaterialScope, fileID string) (DeliverableFileRow, error) {
	ids, files, err := materialArgs(scope)
	if err != nil {
		return DeliverableFileRow{}, err
	}
	var id string
	if err = q.QueryRowContext(ctx, materialScopeSQL+`SELECT id FROM uploads_scope WHERE id=?3`, ids, files, fileID).Scan(&id); err != nil {
		return DeliverableFileRow{}, notFound(err, "uploaded file "+fileID)
	}
	return GetDeliverableFile(ctx, q, id)
}

func MaterialFileReferences(ctx context.Context, q Querier, scope MaterialScope, fileID string) ([]protocol.IssueMaterialSource, error) {
	ids, files, err := materialArgs(scope)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, materialScopeSQL+`SELECT 'upload',f.task_id,'','','run:'||f.run_id,0,'' FROM deliverable_files f WHERE f.file_id=?3 AND f.file_id IN (SELECT id FROM files_scope)
 UNION ALL SELECT 'artifact',a.task_id,a.artifact_id,'','',0,'' FROM artifacts a,json_each(a.ref_json,'$.files') f WHERE a.artifact_id IN (SELECT id FROM artifacts_scope) AND json_extract(f.value,'$.file_id')=?3
 UNION ALL SELECT 'acceptance',a.task_id,'',a.acceptance_id,'',0,'' FROM acceptances a,json_each(a.files_json) f WHERE (a.task_id IN (SELECT id FROM tasks_scope) OR a.artifact_id IN (SELECT id FROM artifacts_scope)) AND json_extract(f.value,'$.file_id')=?3
 UNION ALL SELECT 'task_input',s.task_id,'','',CAST(s.spec_version AS TEXT),0,'' FROM task_specs s,json_each(s.body_json,'$.inputs') f WHERE s.task_id IN (SELECT id FROM tasks_scope) AND instr(s.body_json,?3)>0 AND json_extract(f.value,'$.file_id')=?3
 UNION ALL SELECT kind,task_id,'','',locator,spec_version,issue_id FROM background_refs WHERE id=?3 LIMIT 101`, ids, files, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.IssueMaterialSource{}
	for rows.Next() {
		var source protocol.IssueMaterialSource
		source.Status = "retained"
		if err = rows.Scan(&source.Kind, &source.TaskID, &source.ArtifactID, &source.AcceptanceID, &source.Locator, &source.ContentRevision, &source.IssueID); err != nil {
			return nil, err
		}
		if source.Kind == "upload" {
			source.Status = "uploaded"
		}
		if source.Kind == "task_context" {
			source.Status = "frozen"
		}
		out = append(out, source)
	}
	return out, rows.Err()
}
