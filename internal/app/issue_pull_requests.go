package app

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func registerIssuePullRequest(ctx context.Context, x *Exec, tx *store.Tx, issue protocol.Issue, p protocol.UpdateIssueDeliveryPayload) (Outcome, error) {
	ref, err := protocol.ParseGitHubPullRequestURL(p.PRURL)
	if err != nil {
		return Outcome{}, err
	}
	for _, prior := range issue.Plan.Deliveries {
		if prior.Provider == ref.Provider && prior.Number == ref.Number && strings.EqualFold(prior.Repository, ref.Repository) {
			return Outcome{Result: prior}, nil
		}
	}
	// Reuse the repository spelling of any already observed GitHub identity.
	// The existing provider index keys the shared observation by that spelling.
	var existing string
	err = tx.Q().QueryRowContext(ctx, `SELECT repository FROM server_delivery_jobs
 WHERE provider=? AND number=? AND repository=? COLLATE NOCASE
 ORDER BY repository LIMIT 1`, ref.Provider, ref.Number, ref.Repository).Scan(&existing)
	if err == nil {
		ref.Repository = existing
	} else if errors.Is(err, sql.ErrNoRows) {
		ref.Repository = strings.ToLower(ref.Repository)
	} else {
		return Outcome{}, err
	}
	ref.DeliveryID = "github:" + strings.ToLower(ref.Repository) + "#" + strconv.FormatInt(ref.Number, 10)
	// A legacy caller could choose an arbitrary delivery ID.
	for _, prior := range issue.Plan.Deliveries {
		if prior.DeliveryID == ref.DeliveryID {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "delivery ID is already bound to a different provider identity")
		}
	}
	expected := issue.Revision
	issue.Plan.Deliveries = append(issue.Plan.Deliveries, ref)
	issue.Revision++
	issue.UpdatedAt = x.Now.UnixMilli()
	if err := tx.SaveCanonicalIssue(ctx, issue, expected, store.IssueRevisionProvenance{
		RequestDomain: x.RequestDomain(), Actor: x.ActualPrincipal(), RequestID: x.Env.RequestID,
		SourceMessageID: p.SourceMessageID, CoordinationExecutionID: x.coordinationID, EffectSeq: x.coordinationEffectSeq, Reason: p,
	}); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: ref, Events: []Event{{ObjectType: "issue", ObjectID: issue.IssueID,
		ObjectVersion: issue.Revision, Kind: "issue.updated", Body: map[string]any{"revision": issue.Revision, "delivery_id": ref.DeliveryID},
	}}}, nil
}
