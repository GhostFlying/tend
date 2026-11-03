package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	var applicable bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_controls WHERE issue_id=? AND assignment_epoch=? AND assignment_state='active')`, result.IssueID, result.AssignmentEpoch).Scan(&applicable); err != nil {
		return result, err
	}
	result.Applicable = result.Status == "active" && applicable
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

// ArtifactShareReadable never widens permission to the source task or siblings.
func ArtifactShareReadable(ctx context.Context, q Querier, issue, artifact string, epoch int64) (bool, error) {
	var allowed bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artifact_shares s JOIN issue_controls c ON c.issue_id=s.issue_id JOIN artifacts a ON a.artifact_id=s.artifact_id WHERE s.issue_id=? AND s.artifact_id=? AND s.status='active' AND c.assignment_state='active' AND c.assignment_epoch=s.assignment_epoch AND s.assignment_epoch=? AND a.ref_json=s.ref_json)`, issue, artifact, epoch).Scan(&allowed)
	return allowed, err
}

func ArtifactRefSHA256(ref string) string {
	sum := sha256.Sum256([]byte(ref))
	return hex.EncodeToString(sum[:])
}

// ArtifactFileSharedWithIssue resolves only a file actually frozen in a live
// exact artifact share. It does not expose other uploads from the source run.
func ArtifactFileSharedWithIssue(ctx context.Context, q Querier, issue string, epoch int64, file protocol.ArtifactFile) (bool, error) {
	var allowed bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM artifact_shares s JOIN issue_controls c ON c.issue_id=s.issue_id
 JOIN artifacts a ON a.artifact_id=s.artifact_id, json_each(s.ref_json,'$.files') f
 WHERE s.issue_id=? AND s.assignment_epoch=? AND c.assignment_epoch=s.assignment_epoch
 AND c.assignment_state='active' AND s.status='active' AND a.ref_json=s.ref_json
 AND json_extract(f.value,'$.file_id')=? AND json_extract(f.value,'$.name')=?
 AND json_extract(f.value,'$.size')=? AND json_extract(f.value,'$.sha256')=?)`, issue, epoch, file.FileID, file.Name, file.Size, file.SHA256).Scan(&allowed)
	return allowed, err
}
