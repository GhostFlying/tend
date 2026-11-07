package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

func semanticReviewFacts(facts []protocol.SharedFact) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, fact := range facts {
		if fact.Kind != protocol.SharedFactGoal && fact.Kind != protocol.SharedFactUserConstraint && fact.Kind != protocol.SharedFactDecision && fact.Kind != protocol.SharedFactInput {
			continue
		}
		text := string(fact.Kind) + ":" + fact.Text
		if !seen[text] {
			seen[text] = true
			out = append(out, text)
		}
	}
	slices.Sort(out)
	return out
}
func IssuePRScopeDigest(p protocol.IssuePlan) string {
	criteria := slices.Clone(p.CompletionCriteria)
	slices.Sort(criteria)
	raw, _ := json.Marshal(struct {
		Intent     string
		Scope      string
		Criteria   []string
		Background []string
	}{p.Intent, p.Scope, criteria, semanticReviewFacts(p.Background)})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func IssuePRPolicyDigest(p *protocol.IssuePRPolicy) string {
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// EffectiveIssuePRPolicy inherits only an explicit ancestor policy; no missing
// historical value is interpreted as automatic integration authority.
func EffectiveIssuePRPolicy(ctx context.Context, q Querier, issue protocol.Issue) (protocol.Issue, *protocol.IssuePRPolicy, error) {
	for {
		if issue.Plan.PRPolicy != nil || issue.ParentIssueID == "" {
			return issue, issue.Plan.PRPolicy, nil
		}
		var err error
		issue, err = GetCanonicalIssue(ctx, q, issue.ParentIssueID)
		if err != nil {
			return issue, nil, err
		}
	}
}

// DeliveryOwnerOption retains the exact historical approval and source checks.
func DeliveryOwnerOption(ctx context.Context, q Querier, d protocol.IssueDeliveryDuty) (string, error) {
	if d.Candidate == nil || d.AttentionID == "" {
		return "", nil
	}
	a, err := GetAttention(ctx, q, d.AttentionID)
	if err != nil {
		return "", err
	}
	decision, err := GetDecision(ctx, q, a.DecisionID)
	if err != nil {
		return "", err
	}
	if decision.Status != DecisionAnswered || decision.Version != 2 || decision.Question != protocol.DeliveryCandidateQuestion(*d.Candidate) {
		return "", nil
	}
	var answer struct {
		Option string `json:"option"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal([]byte(decision.AnswerJSON), &answer); err != nil {
		return "", err
	}
	if answer.Text != "" {
		return "", nil
	}
	var current bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_message_controls c USING(message_id) LEFT JOIN message_dispositions d USING(message_id) WHERE m.message_id=? AND m.author='owner' AND COALESCE(c.withdrawn_at,0)=0 AND COALESCE(d.content_revision,1)=(SELECT content_revision FROM issue_delivery_answers WHERE decision_id=? AND message_id=m.message_id))`, decision.AnsweredMessageID, decision.DecisionID).Scan(&current)
	if err != nil || !current {
		return "", err
	}
	return answer.Option, nil
}

// DeliveryGrantValid is the common read-only authority check for workers,
// admission and completion. Automatic merge authority never permits deployment.
func DeliveryGrantValid(ctx context.Context, q Querier, issue protocol.Issue, d protocol.IssueDeliveryDuty) error {
	if d.Authorization == nil || d.Authorization.Mode == "manual" {
		option, err := DeliveryOwnerOption(ctx, q, d)
		if err != nil {
			return err
		}
		if option != "approve" {
			return protocol.Errorf(protocol.CodeForbidden, "delivery needs the original versioned owner approval")
		}
		return nil
	}
	g := d.Authorization
	if g.Mode != "auto" || d.Candidate == nil || d.Candidate.Deploy {
		return protocol.Errorf(protocol.CodeForbidden, "automatic integration does not authorize deployment")
	}
	owner, p, err := EffectiveIssuePRPolicy(ctx, q, issue)
	if err != nil {
		return err
	}
	if p == nil || p.Mode != "auto" || p.Authority == nil || owner.IssueID != g.PolicyIssueID || IssuePRPolicyDigest(p) != g.PolicyDigest || p.Authority.RequestID != g.RequestID || IssuePRScopeDigest(owner.Plan) != p.Authority.ScopeDigest {
		return protocol.Errorf(protocol.CodeForbidden, "Issue integration authority changed or was withdrawn")
	}
	var found bool
	for _, ref := range issue.Plan.Deliveries {
		found = found || ref.DeliveryID == d.Candidate.DeliveryID && ref.Repository == p.Repository
	}
	if !found {
		return protocol.Errorf(protocol.CodeForbidden, "delivery repository is outside the Issue integration grant")
	}
	var valid bool
	if p.Authority.SourceMessageID != "" {
		err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_message_controls c USING(message_id) LEFT JOIN message_dispositions d USING(message_id) WHERE m.message_id=? AND m.author='owner' AND COALESCE(c.withdrawn_at,0)=0 AND COALESCE(d.content_revision,1)=?)`, p.Authority.SourceMessageID, p.Authority.SourceRevision).Scan(&valid)
	} else {
		err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM request_records WHERE request_id=? AND json_extract(principal_json,'$.kind')='owner' AND status IN ('succeeded','accepted'))`, p.Authority.RequestID).Scan(&valid)
	}
	if err != nil {
		return err
	}
	if !valid {
		return protocol.Errorf(protocol.CodeForbidden, "original Issue integration authorization is no longer valid")
	}
	if strings.TrimSpace(p.Repository) == "" {
		return protocol.Errorf(protocol.CodeForbidden, "Issue integration grant has no repository")
	}
	return nil
}
