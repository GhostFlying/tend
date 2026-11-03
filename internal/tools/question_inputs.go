package tools

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
)

type questionInputsReadArgs struct {
	DecisionID string `json:"decision_id"`
}

func (s *Service) questionInputsRead(ctx context.Context, c Call) (any, error) {
	if c.Principal.Kind != protocol.PrincipalOwner && c.Principal.Kind != protocol.PrincipalMainAgent {
		return nil, protocol.Errorf(protocol.CodeForbidden, "only the owner or main coordinator may inspect question input grants")
	}
	var p questionInputsReadArgs
	if err := decode(c.Arguments, &p); err != nil {
		return nil, err
	}
	return s.app.QuestionInputs(ctx, p.DecisionID)
}
