package tools

import (
	"context"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type choiceListArgs struct {
	WorkItemID string `json:"work_item_id"`
}
type choiceCreateArgs struct {
	WorkItemID              string                               `json:"work_item_id"`
	ExpectedControlRevision int64                                `json:"expected_control_revision"`
	Question                protocol.CreateWorkItemChoicePayload `json:"question"`
	Reason                  string                               `json:"reason"`
}
type choiceUpdateArgs struct {
	ChoiceID       string                                `json:"choice_id"`
	Version        int64                                 `json:"version"`
	Answer         string                                `json:"answer,omitempty"`
	OwnerMessageID string                                `json:"owner_message_id,omitempty"`
	Replacement    *protocol.CreateWorkItemChoicePayload `json:"replacement,omitempty"`
	Reason         string                                `json:"reason"`
}

func (s *Service) workItemChoicesList(ctx context.Context, c Call) (any, error) {
	var a choiceListArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	var page protocol.WorkItemChoicePage
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		page.Items, err = store.ListWorkItemChoices(ctx, tx.Q(), a.WorkItemID, "")
		return err
	})
	return page, err
}
func (s *Service) workItemChoicesCreate(ctx context.Context, c Call) (any, error) {
	var a choiceCreateArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	return s.execute(ctx, c, "work_item_choices.create", protocol.Target{Type: "work_item", ID: a.WorkItemID}, &a.ExpectedControlRevision, a.Reason, a.Question, app.CreateWorkItemChoice{})
}
func (s *Service) choiceUpdate(ctx context.Context, c Call, action string, fromMessage bool) (any, error) {
	var a choiceUpdateArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	cmd := app.UpdateWorkItemChoice{Action: action, FromMessage: fromMessage}
	return s.execute(ctx, c, cmd.Operation(), protocol.Target{Type: "work_item_choice", ID: a.ChoiceID}, &a.Version, a.Reason, protocol.UpdateWorkItemChoicePayload{Answer: a.Answer, OwnerMessageID: a.OwnerMessageID, Replacement: a.Replacement}, cmd)
}
func (s *Service) workItemChoicesResolve(ctx context.Context, c Call) (any, error) {
	return s.choiceUpdate(ctx, c, "respond", true)
}
func (s *Service) workItemChoicesWithdraw(ctx context.Context, c Call) (any, error) {
	return s.choiceUpdate(ctx, c, "withdraw", false)
}
func (s *Service) workItemChoicesReplace(ctx context.Context, c Call) (any, error) {
	return s.choiceUpdate(ctx, c, "replace", false)
}
