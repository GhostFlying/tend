package tools

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
)

type handoffAuditArgs struct {
	Instance string `json:"instance"`
	After    int64  `json:"after,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

func (s *Service) handoffAudit(ctx context.Context, c Call) (any, error) {
	if c.Principal.Kind != protocol.PrincipalMainAgent && c.Principal.Kind != protocol.PrincipalOwner {
		return nil, protocol.Errorf(protocol.CodeForbidden, "only owner or main can read conversation maintenance audit")
	}
	var p handoffAuditArgs
	if err := decode(c.Arguments, &p); err != nil {
		return nil, err
	}
	if p.Limit == 0 {
		p.Limit = 50
	}
	return s.app.HandoffAudit(ctx, p.Instance, p.After, p.Limit)
}
