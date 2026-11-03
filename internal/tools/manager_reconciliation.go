package tools

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
)

type managerReconciliationReadArgs struct {
	TaskID string `json:"task_id"`
	RunID  string `json:"run_id"`
}

func (s *Service) tasksManagerReconciliation(ctx context.Context, c Call) (any, error) {
	if c.Principal.Kind != protocol.PrincipalMainAgent && c.Principal.Kind != protocol.PrincipalOwner {
		return nil, protocol.Errorf(protocol.CodeForbidden, "only the owner or main coordinator can inspect manager reconciliation")
	}
	var p managerReconciliationReadArgs
	if err := decode(c.Arguments, &p); err != nil {
		return nil, err
	}
	return s.app.ManagerRunReconciliation(ctx, p.TaskID, p.RunID)
}
