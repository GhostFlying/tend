package tools

import (
	"context"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type artifactShareListArgs struct {
	IssueID string `json:"issue_id"`
	Status  string `json:"status,omitempty"`
	After   string `json:"after,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

func (s *Service) hostedArtifactShares(ctx context.Context, c Call, execution store.HostedExecution) (harness.ToolResult, bool) {
	if c.Tool != "artifact_shares_list" {
		return harness.ToolResult{}, false
	}
	if execution.Selection.Purpose != agentconfig.Main && execution.Selection.Purpose != agentconfig.IssueManager {
		return hostedAnswer(nil, protocol.Errorf(protocol.CodeForbidden, "this role cannot read artifact shares")), true
	}
	var p artifactShareListArgs
	if err := decode(c.Arguments, &p); err != nil {
		return hostedAnswer(nil, err), true
	}
	if p.IssueID == "" {
		p.IssueID = execution.IssueID
	}
	if p.Limit == 0 {
		p.Limit = 50
	}
	var page protocol.ArtifactSharePage
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		current, err := store.GetCanonicalIssue(ctx, tx.Q(), p.IssueID)
		if err != nil {
			return err
		}
		if execution.Selection.Purpose == agentconfig.IssueManager && (execution.IssueID != p.IssueID || execution.AssignmentEpoch != current.AssignmentEpoch || current.AssignmentState != "active") {
			return protocol.Errorf(protocol.CodeForbidden, "share inventory requires the current target responsibility")
		}
		page, err = store.ListArtifactShares(ctx, tx.Q(), p.IssueID, p.After, p.Status, p.Limit)
		return err
	})
	return hostedAnswer(page, err), true
}
