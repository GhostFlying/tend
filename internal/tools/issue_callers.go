package tools

import (
	"context"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func (s *Service) canonicalIssueRead(ctx context.Context, c Call, execution store.HostedExecution) (harness.ToolResult, bool) {
	switch c.Tool {
	case "issues_list", "issues_context_read", "issues_followups_list", "issues_followups_history", "issues_deliveries_read":
	default:
		return harness.ToolResult{}, false
	}
	if execution.Selection.Purpose != agentconfig.Main && execution.Selection.Purpose != agentconfig.IssueManager {
		return hostedAnswer(nil, protocol.Errorf(protocol.CodeForbidden, "consultation has no Issue planning reads")), true
	}
	var value any
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		if c.Tool == "issues_list" {
			var p hostedIssueListArgs
			if err := decode(c.Arguments, &p); err != nil {
				return err
			}
			var err error
			value, err = views.CanonicalIssues(ctx, tx.Q(), views.CanonicalIssueFilter{ReadRoot: execution.IssueID, Project: p.Project, ParentID: p.ParentID, State: p.State, IncludeClosed: p.IncludeClosed, After: p.After, Limit: p.Limit})
			return err
		}
		var p hostedIssueResourceArgs
		if err := decode(c.Arguments, &p); err != nil {
			return err
		}
		if p.IssueID == "" {
			p.IssueID = execution.IssueID
		}
		if p.IssueID == "" {
			return protocol.Errorf(protocol.CodeInvalidRequest, "issue_id is required")
		}
		if execution.IssueID != "" {
			var allowed bool
			if err := tx.Q().QueryRowContext(ctx, `WITH RECURSIVE tree(id) AS (SELECT ? UNION ALL SELECT i.issue_id FROM issue_controls i JOIN tree p ON i.parent_issue_id=p.id) SELECT EXISTS(SELECT 1 FROM tree WHERE id=?)`, execution.IssueID, p.IssueID).Scan(&allowed); err != nil {
				return err
			}
			if !allowed {
				return protocol.Errorf(protocol.CodeForbidden, "Issue is outside manager read scope")
			}
		}
		var err error
		switch c.Tool {
		case "issues_context_read":
			value, err = views.CanonicalIssueContext(ctx, tx.Q(), p.IssueID, p.AfterSource)
		case "issues_followups_list":
			value, err = views.CanonicalIssueFollowups(ctx, tx.Q(), p.IssueID, p.After, s.app.FollowupsEnabled())
		case "issues_followups_history":
			value, err = views.CanonicalIssueFollowupHistory(ctx, tx.Q(), p.IssueID, p.RuleID, p.AfterOccurrence, p.AfterRevision)
		case "issues_deliveries_read":
			value, err = views.CanonicalIssueDeliveries(ctx, tx.Q(), p.IssueID)
		}
		return err
	})
	return hostedAnswer(value, err), true
}
