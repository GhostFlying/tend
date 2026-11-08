package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

func GetArtifactShare(ctx context.Context, q Querier, id string) (protocol.ArtifactShare, error) {
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT state_json FROM artifact_shares WHERE share_id=?`, id).Scan(&raw); err != nil {
		return protocol.ArtifactShare{}, notFound(err, "artifact share "+id)
	}
	var result protocol.ArtifactShare
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return result, fmt.Errorf("store: decode artifact share: %w", err)
	}
	// Assignment changes transfer responsibility, not erase already shared
	// knowledge. Revocation remains the explicit end of this reference.
	result.Applicable = result.Status == "active"
	return result, nil
}

func (t *Tx) PutArtifactShare(ctx context.Context, v protocol.ArtifactShare, ref, request string) error {
	// Applicability is a current read projection, never frozen as authority.
	v.Applicable = false
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if v.Revision == 1 {
		_, err = t.Exec(ctx, `INSERT INTO artifact_shares(share_id,issue_id,assignment_epoch,artifact_id,revision,status,state_json,ref_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, v.ShareID, v.IssueID, v.AssignmentEpoch, v.ArtifactID, v.Revision, v.Status, string(raw), ref, v.CreatedAt, v.UpdatedAt)
	} else {
		var result interface{ RowsAffected() (int64, error) }
		result, err = t.Exec(ctx, `UPDATE artifact_shares SET revision=?,status=?,state_json=?,updated_at=? WHERE share_id=? AND revision=?`, v.Revision, v.Status, string(raw), v.UpdatedAt, v.ShareID, v.Revision-1)
		if err == nil {
			n, e := result.RowsAffected()
			if e != nil {
				return e
			}
			if n != 1 {
				return protocol.Errorf(protocol.CodeStaleRevision, "artifact share changed")
			}
		}
	}
	if err != nil {
		return fmt.Errorf("store: save artifact share: %w", err)
	}
	_, err = t.Exec(ctx, `INSERT INTO artifact_share_revisions(share_id,revision,request_id,state_json) VALUES(?,?,?,?)`, v.ShareID, v.Revision, request, string(raw))
	return err
}

func ListArtifactShares(ctx context.Context, q Querier, issue, after, status string, limit int) (protocol.ArtifactSharePage, error) {
	page := protocol.ArtifactSharePage{Items: []protocol.ArtifactShare{}}
	if limit < 1 || limit > 100 {
		return page, protocol.Errorf(protocol.CodeInvalidRequest, "share limit must be between 1 and 100")
	}
	if status != "" && status != "active" && status != "revoked" {
		return page, protocol.Errorf(protocol.CodeInvalidRequest, "invalid share status")
	}
	rows, err := q.QueryContext(ctx, `SELECT share_id FROM artifact_shares WHERE issue_id=? AND share_id>? AND (?='' OR status=?) ORDER BY share_id LIMIT ?`, issue, after, status, status, limit+1)
	if err != nil {
		return page, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return page, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return page, err
	}
	if err := rows.Close(); err != nil {
		return page, err
	}
	if len(ids) > limit {
		ids = ids[:limit]
		page.Next = ids[len(ids)-1]
	}
	for _, id := range ids {
		v, err := GetArtifactShare(ctx, q, id)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, v)
	}
	return page, nil
}

// ArtifactShareReadable includes ordinary same-project knowledge and exact
// cross-project shares. A target's explicit revocation remains an exception.
func ArtifactShareReadable(ctx context.Context, q Querier, issue, artifact string, _ int64) (bool, error) {
	var allowed bool
	err := q.QueryRowContext(ctx, `SELECT (
 EXISTS(SELECT 1 FROM artifacts a JOIN task_work_item_owners o ON o.task_id=a.task_id JOIN work_items source ON source.work_item_id=o.work_item_id JOIN work_items target ON target.work_item_id=? WHERE a.artifact_id=? AND source.project<>'' AND source.project=target.project)
 OR EXISTS(SELECT 1 FROM artifact_shares s JOIN artifacts a ON a.artifact_id=s.artifact_id WHERE s.issue_id=? AND s.artifact_id=? AND s.status='active' AND a.ref_json=s.ref_json))
 AND NOT EXISTS(SELECT 1 FROM artifact_shares s WHERE s.issue_id=? AND s.artifact_id=? AND s.status='revoked' AND NOT EXISTS(SELECT 1 FROM artifact_shares active WHERE active.issue_id=s.issue_id AND active.artifact_id=s.artifact_id AND active.status='active'))`, issue, artifact, issue, artifact, issue, artifact).Scan(&allowed)
	return allowed, err
}

func ArtifactRefSHA256(ref string) string {
	sum := sha256.Sum256([]byte(ref))
	return hex.EncodeToString(sum[:])
}

// ArtifactFileSharedWithIssue resolves a file actually submitted in readable
// project knowledge or an exact share. It never exposes an unsubmitted upload.
func ArtifactFileSharedWithIssue(ctx context.Context, q Querier, issue string, _ int64, file protocol.ArtifactFile) (bool, error) {
	frozen, artifact, err := GetFinalizedDeliverableFile(ctx, q, file.FileID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if frozen.ArtifactFile != file {
		return false, nil
	}
	return ArtifactShareReadable(ctx, q, issue, artifact, 0)
}
