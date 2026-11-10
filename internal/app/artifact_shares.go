package app

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

type ArtifactShareCommand struct{ Revoke bool }

func (c ArtifactShareCommand) Operation() string {
	if c.Revoke {
		return protocol.OpRevokeArtifactShare
	}
	return protocol.OpGrantArtifactShare
}

func (ArtifactShareCommand) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}

func (c ArtifactShareCommand) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "issue" || x.Env.ParentRequestID != "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "independent target Issue request required")
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	issue, err := store.GetCanonicalIssue(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, errNotFound(err, "target Issue does not exist")
	}
	if issue.Revision != expected {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "target Issue changed")
	}
	var grant protocol.GrantArtifactSharePayload
	var revoke protocol.RevokeArtifactSharePayload
	control := &grant.IssueControlPayload
	if c.Revoke {
		if err := decodePayload(x.Env, &revoke); err != nil {
			return Outcome{}, err
		}
		control = &revoke.IssueControlPayload
	} else if err := decodePayload(x.Env, &grant); err != nil {
		return Outcome{}, err
	}
	if issue.AssignmentEpoch != control.ExpectedAssignmentEpoch || issue.AssignmentState != "active" {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "target responsibility changed")
	}
	if strings.TrimSpace(control.Reason) == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "explicit sharing reason required")
	}
	if err := validateCanonicalSourceReference(ctx, tx.Q(), canonicalOwnerIssueSource(control.IssueSourceReference)); err != nil {
		return Outcome{}, err
	}
	if x.Principal.Kind != protocol.PrincipalOwner {
		if x.Principal.Kind != protocol.PrincipalMainAgent || x.coordinationExecution == nil || x.coordinationExecution.Selection.Purpose != agentconfig.Main || control.SourceMessageID == "" {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "only an admitted main execution can relay explicit owner sharing")
		}
		if err := validateAttentionOwnerRelay(ctx, tx.Q(), x, issue.IssueID, control.SourceMessageID); err != nil {
			return Outcome{}, err
		}
	}
	var value protocol.ArtifactShare
	var ref string
	if c.Revoke {
		value, err = store.GetArtifactShare(ctx, tx.Q(), revoke.ShareID)
		if err != nil {
			return Outcome{}, errNotFound(err, "artifact share does not exist")
		}
		if value.IssueID != issue.IssueID {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "share belongs to another Issue")
		}
		if value.Revision != revoke.ExpectedShareRevision {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "share changed")
		}
		if value.Status != "active" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "share is already revoked")
		}
		value.Status = "revoked"
		value.Revision++
		value.Reason = control.Reason
		value.UpdatedAt = x.Now.UnixMilli()
	} else {
		if issue.State != protocol.IssueActive && issue.State != protocol.IssuePaused {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "reopen target responsibility before granting evidence")
		}
		if !protocol.ValidID(protocol.PrefixArtifact, grant.ArtifactID) || !release.ValidSHA(grant.RefSHA256) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "exact artifact and ref SHA256 required")
		}
		artifact, err := store.GetArtifact(ctx, tx.Q(), grant.ArtifactID)
		if err != nil {
			return Outcome{}, errNotFound(err, "source artifact does not exist")
		}
		if store.ArtifactRefSHA256(artifact.RefJSON) != grant.RefSHA256 {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "artifact reference differs from approved bytes")
		}
		source, err := store.GetTaskIssueOwner(ctx, tx.Q(), artifact.TaskID)
		if err != nil {
			return Outcome{}, errNotFound(err, "artifact source Issue does not exist")
		}
		var payload struct {
			Files []protocol.ArtifactFile `json:"files"`
		}
		if err := json.Unmarshal([]byte(artifact.RefJSON), &payload); err != nil {
			return Outcome{}, err
		}
		files := make([]protocol.ArtifactFile, 0, len(payload.Files))
		seen := map[string]bool{}
		for _, file := range payload.Files {
			actual, err := store.GetDeliverableFile(ctx, tx.Q(), file.FileID)
			if err != nil {
				return Outcome{}, errNotFound(err, "artifact file does not exist")
			}
			if seen[file.FileID] || actual.TaskID != artifact.TaskID || actual.RunID != artifact.RunID || actual.ArtifactFile != file {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "artifact file provenance differs")
			}
			seen[file.FileID] = true
			files = append(files, file)
		}
		var duplicate bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artifact_shares WHERE issue_id=? AND assignment_epoch=? AND artifact_id=? AND status='active')`, issue.IssueID, issue.AssignmentEpoch, artifact.ArtifactID).Scan(&duplicate); err != nil {
			return Outcome{}, err
		}
		if duplicate {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "an active exact artifact share already exists")
		}
		value = protocol.ArtifactShare{ShareID: x.IDs.New(protocol.PrefixArtifactShare), Revision: 1, IssueID: issue.IssueID, AssignmentEpoch: issue.AssignmentEpoch, ArtifactID: artifact.ArtifactID, SourceTaskID: artifact.TaskID, SourceIssueID: source, Digest: artifact.Digest, RefSHA256: grant.RefSHA256, Files: files, Status: "active", Reason: control.Reason, SourceMessageID: control.SourceMessageID, SourceRevision: control.ExpectedSourceRevision, CreatedAt: x.Now.UnixMilli(), UpdatedAt: x.Now.UnixMilli()}
		ref = artifact.RefJSON
	}
	if err := tx.PutArtifactShare(ctx, value, ref, x.Env.RequestID); err != nil {
		return Outcome{}, err
	}
	issue.Revision++
	issue.UpdatedAt = x.Now.UnixMilli()
	// Authorization provenance is retained in the exact share and revision reason.
	// SourceMessageID also grants general message/file material access, which an
	// artifact-only approval must never confer (including after revocation).
	if err := tx.SaveCanonicalIssue(ctx, issue, expected, store.IssueRevisionProvenance{RequestDomain: x.RequestDomain(), Actor: x.ActualPrincipal(), RequestID: x.Env.RequestID, CoordinationExecutionID: x.coordinationID, EffectSeq: x.coordinationEffectSeq, Reason: control}); err != nil {
		return Outcome{}, err
	}
	value.Applicable = value.Status == "active" && value.AssignmentEpoch == issue.AssignmentEpoch
	return Outcome{Result: value, Events: []Event{{ObjectType: "issue", ObjectID: issue.IssueID, ObjectVersion: issue.Revision, Kind: c.Operation(), Body: map[string]any{"share_id": value.ShareID, "revision": value.Revision, "artifact_id": value.ArtifactID, "status": value.Status}}}}, nil
}
