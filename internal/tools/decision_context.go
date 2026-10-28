package tools

import (
	"context"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

type explainDecisionArgs struct {
	DecisionID              string                   `json:"decision_id"`
	ExpectedControlRevision int64                    `json:"expected_control_revision"`
	DecisionVersion         int64                    `json:"decision_version"`
	Summary                 string                   `json:"summary"`
	OwnerChoice             string                   `json:"owner_choice"`
	Issues                  []protocol.DecisionIssue `json:"issues"`
}

func (s *Service) decisionsExplain(ctx context.Context, c Call) (any, error) {
	var a explainDecisionArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	if err := requireMessage(c); err != nil {
		return nil, err
	}
	return s.execute(ctx, c, "decisions.explain", protocol.Target{Type: "decision", ID: a.DecisionID}, &a.ExpectedControlRevision, "explain current decision issues and owner choice", protocol.ExplainDecisionPayload{DecisionVersion: a.DecisionVersion, Summary: a.Summary, OwnerChoice: a.OwnerChoice, Issues: a.Issues, SourceMessageID: c.SourceMessageID}, app.ExplainDecision{})
}
