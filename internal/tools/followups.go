package tools

import (
	"context"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type followupListArgs struct {
	WorkItemID string `json:"work_item_id"`
	After      string `json:"after,omitempty"`
}
type followupHistoryArgs struct {
	RuleID          string `json:"rule_id"`
	AfterRevision   int64  `json:"after_revision,omitempty"`
	AfterOccurrence string `json:"after_occurrence,omitempty"`
}
type followupCreateArgs struct {
	WorkItemID              string                `json:"work_item_id"`
	ExpectedControlRevision int64                 `json:"expected_control_revision"`
	Spec                    protocol.FollowupSpec `json:"spec"`
	Reason                  string                `json:"reason"`
}
type followupControlArgs struct {
	RuleID                  string                 `json:"rule_id"`
	ExpectedControlRevision int64                  `json:"expected_control_revision"`
	Spec                    *protocol.FollowupSpec `json:"spec,omitempty"`
	Reason                  string                 `json:"reason"`
}

func (s *Service) followupsList(ctx context.Context, c Call) (any, error) {
	var a followupListArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	var p protocol.FollowupPage
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		p, err = store.ListFollowups(ctx, tx.Q(), a.WorkItemID, a.After)
		return err
	})
	p.Enabled = s.app.FollowupsEnabled() && !c.Principal.Addon()
	return p, err
}
func (s *Service) followupsHistory(ctx context.Context, c Call) (any, error) {
	var a followupHistoryArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	var p protocol.FollowupHistoryPage
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		p, err = store.FollowupHistory(ctx, tx.Q(), a.RuleID, a.AfterOccurrence, a.AfterRevision)
		return err
	})
	return p, err
}
func (s *Service) followupsCreate(ctx context.Context, c Call) (any, error) {
	var a followupCreateArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	if err := requireMessage(c); err != nil {
		return nil, err
	}
	a.Spec.SourceID = c.SourceMessageID
	cmd := app.FollowupCommand{Action: "create"}
	return s.execute(ctx, c, cmd.Operation(), protocol.Target{Type: "work_item", ID: a.WorkItemID}, &a.ExpectedControlRevision, a.Reason, a.Spec, cmd)
}
func (s *Service) followupControl(ctx context.Context, c Call, action string) (any, error) {
	var a followupControlArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	if err := requireMessage(c); err != nil {
		return nil, err
	}
	if a.Spec != nil {
		a.Spec.SourceID = c.SourceMessageID
	}
	cmd := app.FollowupCommand{Action: action}
	return s.execute(ctx, c, cmd.Operation(), protocol.Target{Type: "followup", ID: a.RuleID}, &a.ExpectedControlRevision, a.Reason, protocol.UpdateFollowupPayload{Spec: a.Spec}, cmd)
}
func (s *Service) followupsUpdate(ctx context.Context, c Call) (any, error) {
	return s.followupControl(ctx, c, "update")
}
func (s *Service) followupsPause(ctx context.Context, c Call) (any, error) {
	return s.followupControl(ctx, c, "pause")
}
func (s *Service) followupsResume(ctx context.Context, c Call) (any, error) {
	return s.followupControl(ctx, c, "resume")
}
func (s *Service) followupsCancel(ctx context.Context, c Call) (any, error) {
	return s.followupControl(ctx, c, "cancel")
}
func (s *Service) followupsTrigger(ctx context.Context, c Call) (any, error) {
	return s.followupControl(ctx, c, "trigger")
}
