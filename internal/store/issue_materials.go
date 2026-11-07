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
	IssueIDs        []string
	BackgroundFiles map[string][]protocol.IssueMaterialSource
}

// The same scoped candidates back listing and exact metadata/byte reads.
const materialScopeSQL = `WITH scope(id) AS (SELECT value FROM json_each(?)),
 tasks_scope(id) AS (
 SELECT task_id FROM task_work_item_owners WHERE work_item_id IN (SELECT id FROM scope)
 UNION SELECT l.task_id FROM work_item_task_links l WHERE l.work_item_id IN (SELECT id FROM scope) AND NOT EXISTS(SELECT 1 FROM issue_controls c WHERE c.issue_id=l.work_item_id)),
 messages_scope(id) AS (
 SELECT message_id FROM message_work_items WHERE work_item_id IN (SELECT id FROM scope) AND active=1
 UNION SELECT m.message_id FROM agent_conversations c JOIN conversation_messages m USING(conversation_id) WHERE c.work_item_id IN (SELECT id FROM scope)
 UNION SELECT message_id FROM message_context_obligations WHERE work_item_id IN (SELECT id FROM scope)
 UNION SELECT source_message_id FROM issue_control_revisions WHERE issue_id IN (SELECT id FROM scope)
 UNION SELECT source_message_id FROM work_item_context_facts WHERE work_item_id IN (SELECT id FROM scope)
 UNION SELECT s.message_id FROM background_batches b JOIN background_sources s USING(batch_id) WHERE b.work_item_id IN (SELECT id FROM scope)),
 artifacts_scope(id) AS (
 SELECT artifact_id FROM artifacts WHERE task_id IN (SELECT id FROM tasks_scope)
 UNION SELECT a.artifact_id FROM artifacts a JOIN work_item_task_links l ON l.task_id=a.task_id WHERE l.work_item_id IN (SELECT id FROM scope)
 UNION SELECT s.artifact_id FROM artifact_shares s JOIN issue_controls c ON c.issue_id=s.issue_id AND c.assignment_epoch=s.assignment_epoch JOIN artifacts a ON a.artifact_id=s.artifact_id AND a.ref_json=s.ref_json WHERE s.issue_id IN (SELECT id FROM scope) AND s.status='active' AND c.assignment_state='active'),
 files_scope(id) AS (
 SELECT a.file_id FROM conversation_message_attachments a WHERE a.message_id IN (SELECT id FROM messages_scope)
 UNION SELECT json_extract(f.value,'$.file_id') FROM artifacts a,json_each(a.ref_json,'$.files') f WHERE a.artifact_id IN (SELECT id FROM artifacts_scope)
 UNION SELECT json_extract(f.value,'$.file_id') FROM acceptances a,json_each(a.files_json) f WHERE (a.task_id IN (SELECT id FROM tasks_scope) OR a.artifact_id IN (SELECT id FROM artifacts_scope))
 UNION SELECT json_extract(f.value,'$.file_id') FROM task_specs s,json_each(s.body_json,'$.inputs') f WHERE s.task_id IN (SELECT id FROM tasks_scope)
 UNION SELECT value FROM json_each(?)) `

func materialArgs(scope MaterialScope) (string, string, error) {
	ids, err := json.Marshal(scope.IssueIDs)
	if err != nil {
		return "", "", err
	}
	files := make([]string, 0, len(scope.BackgroundFiles))
	for id := range scope.BackgroundFiles {
		files = append(files, id)
	}
	refs, err := json.Marshal(files)
	return string(ids), string(refs), err
}

// WalkMaterialTaskContexts streams only this Issue scope's retained Task
// contexts. All readers use those immutable sources, independent of which Run
// currently asks or whether the current Issue background still lists them.
func WalkMaterialTaskContexts(ctx context.Context, q Querier, scope MaterialScope, visit func(string, int64, protocol.TaskWorkContext)) error {
	ids, files, err := materialArgs(scope)
	if err != nil {
		return err
	}
	rows, err := q.QueryContext(ctx, materialScopeSQL+`SELECT s.task_id,s.spec_version,c.value FROM task_specs s,json_each(s.body_json,'$.shared_context') c WHERE s.task_id IN (SELECT id FROM tasks_scope) ORDER BY s.task_id,s.spec_version,c.key`, ids, files)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var task, raw string
		var version int64
		if err := rows.Scan(&task, &version, &raw); err != nil {
			return err
		}
		var item protocol.TaskWorkContext
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			return fmt.Errorf("store: decode retained Issue context: %w", err)
		}
		visit(task, version, item)
	}
	return rows.Err()
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

func MaterialFileReferences(ctx context.Context, q Querier, scope MaterialScope, fileID string) ([]protocol.IssueMaterialSource, error) {
	ids, files, err := materialArgs(scope)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, materialScopeSQL+`SELECT 'artifact',a.task_id,a.artifact_id,'','' FROM artifacts a,json_each(a.ref_json,'$.files') f WHERE a.artifact_id IN (SELECT id FROM artifacts_scope) AND json_extract(f.value,'$.file_id')=?
 UNION ALL SELECT 'acceptance',a.task_id,'',a.acceptance_id,'' FROM acceptances a,json_each(a.files_json) f WHERE (a.task_id IN (SELECT id FROM tasks_scope) OR a.artifact_id IN (SELECT id FROM artifacts_scope)) AND json_extract(f.value,'$.file_id')=?
 UNION ALL SELECT 'task_input',s.task_id,'','',CAST(s.spec_version AS TEXT) FROM task_specs s,json_each(s.body_json,'$.inputs') f WHERE s.task_id IN (SELECT id FROM tasks_scope) AND json_extract(f.value,'$.file_id')=? LIMIT 101`, ids, files, fileID, fileID, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.IssueMaterialSource{}
	for rows.Next() {
		var source protocol.IssueMaterialSource
		source.Status = "retained"
		if err = rows.Scan(&source.Kind, &source.TaskID, &source.ArtifactID, &source.AcceptanceID, &source.Locator); err != nil {
			return nil, err
		}
		out = append(out, source)
	}
	return out, rows.Err()
}
