package app

import (
	"context"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// RequestIssueManager publishes category-selected work without choosing a runner.
type RequestIssueManager struct{}

// RequestIssueOwner publishes the same continuing execution role for an
// ordinary Agent, with the explicit child-Issue delegation contract.
type RequestIssueOwner struct{}

func (RequestIssueOwner) Operation() string { return protocol.OpIssueRequestOwner }
func (RequestIssueOwner) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	return (RequestIssueManager{}).Authorize(ctx, x, q)
}
func (RequestIssueOwner) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.RequestIssueManagerPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.CategoryID == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "select an active owner capability category with agent_categories_list")
	}
	return selectIssueOwner(ctx, x, tx, p, protocol.IssueOwnerPolicy)
}

func (RequestIssueManager) Operation() string { return protocol.OpIssueRequestManager }
func (RequestIssueManager) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner, protocol.PrincipalMainAgent)
}
func (c RequestIssueManager) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.RequestIssueManagerPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	{
		if p.CategoryID == "" {
			p.CategoryID = protocol.DefaultIssueManagerCategory
		}
		return selectIssueOwner(ctx, x, tx, p, "")
	}
}

func insertManagerAction(ctx context.Context, x *Exec, tx *store.Tx, task string, generation int64) error {
	return tx.InsertAction(ctx, store.ActionRow{ActionID: x.IDs.New(protocol.PrefixAction), TaskID: task, Kind: protocol.ActionCoordinate, EffectKey: fmt.Sprintf("coordinate|%s|%d", task, generation), TriggerJSON: `{}`, TargetSpecVersion: 1, AuthorizationJSON: `{}`, Generation: generation, Status: protocol.ActionPending, CreatedRequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli()})
}
