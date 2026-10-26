package tools

import (
	"context"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

type backgroundCheckinArgs struct {
	Note        string   `json:"note"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
}
type backgroundSourceArgs struct {
	After int64 `json:"after,omitempty"`
}

func (s *Service) background(ctx context.Context, c Call, action string, payload any) (any, error) {
	cmd := app.BackgroundCommand{Action: action, MessageID: c.SourceMessageID, TurnID: c.TurnID, DeliveryID: c.ParentRequestID}
	return s.execute(ctx, c, cmd.Operation(), protocol.Target{Type: "background", ID: c.SourceMessageID}, nil, "", payload, cmd)
}
func (s *Service) backgroundSources(ctx context.Context, c Call) (any, error) {
	var p backgroundSourceArgs
	if err := decode(c.Arguments, &p); err != nil {
		return nil, err
	}
	return s.background(ctx, c, "sources", p)
}
func (s *Service) backgroundCheckin(ctx context.Context, c Call) (any, error) {
	var p backgroundCheckinArgs
	if err := decode(c.Arguments, &p); err != nil {
		return nil, err
	}
	return s.background(ctx, c, "checkin", p)
}
