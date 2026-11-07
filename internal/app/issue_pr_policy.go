package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func validateIssuePRPolicyChange(ctx context.Context, x *Exec, q store.Querier, previous protocol.Issue, next *protocol.Issue) error {
	p := next.Plan.PRPolicy
	if p == nil {
		return nil
	}
	if p.Mode != "auto" && p.Mode != "manual" || strings.TrimSpace(p.Repository) == "" || strings.TrimSpace(p.Repository) != p.Repository || strings.TrimSpace(p.Reason) == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "pr_policy requires mode auto/manual, an exact repository and a reason")
	}
	unchanged := reflect.DeepEqual(previous.Plan.PRPolicy, p)
	if unchanged && (p.Mode != "auto" || p.Authority != nil && p.Authority.ScopeDigest == store.IssuePRScopeDigest(next.Plan)) {
		return nil
	}
	if p.Mode == "manual" {
		// Requiring a real approval is a restriction, never a new provider grant.
		p.Authority = nil
		return nil
	}
	authority := &protocol.IssuePRPolicyAuthority{RequestID: x.Env.RequestID, ScopeDigest: store.IssuePRScopeDigest(next.Plan)}
	if x.Principal.Kind != protocol.PrincipalOwner {
		var source canonicalOwnerIssueSource
		if err := json.Unmarshal(x.Env.Payload, &source); err != nil {
			return protocol.Errorf(protocol.CodeInvalidRequest, "invalid policy source")
		}
		if x.coordinationExecution == nil || source.SourceMessageID == "" || source.ExpectedSourceRevision == nil {
			return protocol.Errorf(protocol.CodeForbidden, "automatic integration requires an authenticated owner request or admitted owner source")
		}
		if x.coordinationExecution.Selection.Purpose == agentconfig.Main {
			admitted, err := mainExecutionSourceAdmitted(ctx, q, x.coordinationExecution.ID, source.SourceMessageID)
			if err != nil {
				return err
			}
			if !admitted {
				return protocol.Errorf(protocol.CodeForbidden, "integration authorization source was not admitted")
			}
		} else if err := hostedSourceInIssueScope(ctx, q, x.coordinationExecution.IssueID, source.SourceMessageID); err != nil {
			return err
		}
		var actualOwner bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN message_dispositions d USING(message_id) LEFT JOIN conversation_message_controls c USING(message_id) WHERE m.message_id=? AND m.author='owner' AND COALESCE(d.content_revision,1)=? AND COALESCE(c.withdrawn_at,0)=0)`, source.SourceMessageID, *source.ExpectedSourceRevision).Scan(&actualOwner); err != nil {
			return err
		}
		if !actualOwner {
			return protocol.Errorf(protocol.CodeForbidden, "automatic integration requires a current original owner source")
		}
		authority.SourceMessageID, authority.SourceRevision = source.SourceMessageID, *source.ExpectedSourceRevision
	}
	copyPolicy := *p
	copyPolicy.Authority = authority
	next.Plan.PRPolicy = &copyPolicy
	return nil
}

func issueWithNextRevision(i protocol.Issue, at int64) protocol.Issue {
	i.Revision++
	i.UpdatedAt = at
	return i
}

func ensureIssuePRPolicyQuestion(ctx context.Context, x *Exec, tx *store.Tx, i protocol.Issue, repository string) (string, []Event, error) {
	var existing string
	err := tx.Q().QueryRowContext(ctx, `SELECT attention_id FROM issue_pr_policy_questions WHERE issue_id=?`, i.IssueID).Scan(&existing)
	if err == nil {
		a, readErr := store.GetAttention(ctx, tx.Q(), existing)
		if readErr != nil {
			return "", nil, readErr
		}
		if a.Status != "withdrawn" {
			return existing, nil, nil
		}
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", nil, err
	}
	inner := *x
	env := *x.Env
	inner.Env = &env
	env.Target = protocol.Target{Type: "attention"}
	env.ExpectedControlRevision = nil
	env.Payload, _ = json.Marshal(protocol.CreateAttentionPayload{IssueID: i.IssueID, Kind: "decision", Title: "Choose this Issue's PR integration policy for " + repository + ": authorize Tend to merge after one shared formal review and required checks, or require a concrete candidate approval. Merge authority does not grant deployment. Preparation, review and CI may continue while this policy is unresolved.", Options: []string{"automatic_merge", "manual_approval", "defer"}, BlocksClose: true})
	out, err := (AttentionCommand{Action: "create"}).Apply(ctx, &inner, tx)
	if err != nil {
		return "", nil, err
	}
	a := out.Result.(protocol.Attention)
	if _, err := tx.Exec(ctx, `INSERT INTO issue_pr_policy_questions(issue_id,attention_id) VALUES(?,?) ON CONFLICT(issue_id) DO UPDATE SET attention_id=excluded.attention_id`, i.IssueID, a.AttentionID); err != nil {
		return "", nil, err
	}
	return a.AttentionID, out.Events, nil
}

func settleIssuePRPolicyQuestion(ctx context.Context, x *Exec, tx *store.Tx, issue string) error {
	var id string
	err := tx.Q().QueryRowContext(ctx, `SELECT attention_id FROM issue_pr_policy_questions WHERE issue_id=?`, issue).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	a, err := store.GetAttention(ctx, tx.Q(), id)
	if err != nil {
		return err
	}
	if a.Status != "open" {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE decisions SET status='cancelled',version=version+1,updated_at=? WHERE decision_id=? AND status='open'`, x.Now.UnixMilli(), a.DecisionID); err != nil {
		return err
	}
	a.Status, a.Resolution = "withdrawn", "The Issue integration policy was explicitly recorded; candidate and deployment approvals remain separate"
	return saveAttention(ctx, x, tx, &a, a.Revision)
}

func deliveryExecutionScope(c protocol.IssueDeliveryCandidate, g *protocol.IssueDeliveryGrant) string {
	if g == nil || g.Mode != "auto" {
		return deliveryCandidateQuestion(c)
	}
	return fmt.Sprintf("The authenticated Issue policy authorizes automatic merge after the one shared formal PR review and exact-head provider checks. Frozen PR %s; head %s; base %s; target %s. Policy source %s, digest %s. No human candidate approval was fabricated. A revised candidate must refresh affected review/check evidence and use a new frozen execution under the still-valid policy; it does not require another human approval solely because the head changed. This grant does not authorize deployment.", c.URL, c.HeadSHA, c.BaseSHA, c.TargetSHA, g.RequestID, g.PolicyDigest)
}
