package app

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func validateDecisionIssues(issues []protocol.DecisionIssue) error {
	if len(issues) > 20 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "at most 20 decision issues")
	}
	seen := map[string]bool{}
	for _, issue := range issues {
		if seen[issue.IssueID] || len(issue.IssueID) > 80 ||
			!slices.Contains([]string{"environment", "evidence", "authorization"}, issue.Category) ||
			!slices.Contains([]string{"worker", "main_agent", "owner"}, issue.Actor) ||
			!slices.Contains([]string{"open", "resolved", "workaround", "deferred"}, issue.Status) {
			return protocol.Errorf(protocol.CodeInvalidRequest, "issues require distinct IDs, category, actor and status")
		}
		seen[issue.IssueID] = true
		for _, text := range []string{issue.IssueID, issue.Summary, issue.Impact, issue.NextAction, issue.Source} {
			if strings.TrimSpace(text) == "" || len(text) > 2000 || !utf8.ValidString(text) {
				return protocol.Errorf(protocol.CodeInvalidRequest, "each issue requires bounded summary, impact, next_action and source")
			}
		}
	}
	return nil
}

// ExplainDecision retains coordinator context without answering or rewriting the
// original question. Control revision belongs to the explanation, not the answer.
type ExplainDecision struct{}

func (ExplainDecision) Operation() string { return "decisions.explain" }
func (ExplainDecision) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalMainAgent)
}
func (ExplainDecision) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.ExplainDecisionPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if err := validateDecisionIssues(p.Issues); err != nil {
		return Outcome{}, err
	}
	for _, text := range []string{p.Summary, p.OwnerChoice} {
		if strings.TrimSpace(text) == "" || len(text) > 4000 || !utf8.ValidString(text) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "summary and owner_choice are required and bounded")
		}
	}
	d, err := store.GetDecision(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, errNotFound(err, "decision does not exist")
	}
	if d.Version != p.DecisionVersion || d.Status != store.DecisionOpen {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "decision changed; reread the current question")
	}
	var revision int64
	if d.Briefing != nil {
		revision = d.Briefing.Revision
	}
	if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "decision explanation changed")
	}
	// Every original issue must have an explicit disposition, even when fixed
	// or no longer blocking. New coordinator findings can be added with sources.
	var originalIssues []protocol.DecisionIssue
	if d.Worker != nil {
		originalIssues = append(originalIssues, d.Worker.Issues...)
	}
	if d.Briefing != nil {
		originalIssues = append(originalIssues, d.Briefing.Issues...)
	}
	{
		for _, original := range originalIssues {
			if !slices.ContainsFunc(p.Issues, func(issue protocol.DecisionIssue) bool { return issue.IssueID == original.IssueID }) {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "explanation must account for issue %s", original.IssueID)
			}
		}
	}
	if _, err := store.GetMessage(ctx, tx.Q(), p.SourceMessageID); err != nil {
		return Outcome{}, errNotFound(err, "explanation requires the current source message")
	}
	b := protocol.DecisionBriefing{Revision: revision + 1, Summary: p.Summary, OwnerChoice: p.OwnerChoice, Issues: p.Issues, SourceMessageID: p.SourceMessageID, CreatedAt: x.Now.UnixMilli()}
	raw, err := json.Marshal(b)
	if err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO decision_briefings(decision_id,revision,body_json) VALUES(?,?,?)`, d.DecisionID, b.Revision, string(raw)); err != nil {
		return Outcome{}, err
	}
	if err := bindSource(ctx, tx, p.SourceMessageID, "decision", d.DecisionID); err != nil {
		return Outcome{}, err
	}
	return Outcome{Notify: true, Result: b, Events: []Event{{ObjectType: "decision", ObjectID: d.DecisionID, ObjectVersion: b.Revision, Kind: "decision.explained", Body: map[string]any{"briefing": b, "decision_version": p.DecisionVersion}}}}, nil
}
