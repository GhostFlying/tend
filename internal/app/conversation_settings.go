package app

import (
	"context"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// ConversationSettings reads the owner's shared next-turn selection.
func (a *App) ConversationSettings(ctx context.Context, session ...string) (store.ConversationSettings, error) {
	var settings store.ConversationSettings
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		settings, err = store.GetConversationSettings(ctx, tx.Q(), session...)
		return err
	})
	return settings, err
}

// SetConversationSettings changes owner-controlled settings (D73). The API
// obtains the catalog before Execute; Apply performs no external IO.
type SetConversationSettings struct {
	Available bool
	Models    []protocol.ConversationModel
}

// Operation implements Command.
func (SetConversationSettings) Operation() string { return "conversation.set_settings" }

// Authorize implements Command, including replays while the catalog is down.
func (SetConversationSettings) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}

// Apply implements Command.
func (c SetConversationSettings) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "conversation" || x.Env.Target.ID != "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "target must be the shared conversation")
	}
	var p protocol.SetConversationSettingsPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	current, err := store.GetConversationSettings(ctx, tx.Q(), p.ConversationID)
	if err != nil {
		return Outcome{}, err
	}
	if expected != current.Revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "conversation settings have changed; read them again")
	}
	if !c.Available {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "the hosted model catalog is not available")
	}
	index := slices.IndexFunc(c.Models, func(m protocol.ConversationModel) bool { return m.ID == p.Model })
	if p.Model == "" || index < 0 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "model is not in the hosted catalog")
	}
	model := c.Models[index]
	supported := slices.Contains(model.SupportedEfforts, p.Effort)
	if len(model.SupportedEfforts) == 0 {
		supported = p.Effort == model.DefaultEffort
	}
	if !supported {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "effort is not supported by this model")
	}
	next := store.ConversationSettings{Model: p.Model, Effort: p.Effort, Revision: current.Revision + 1}
	if store.ConversationID(p.ConversationID) != "main" {
		if _, err := tx.Exec(ctx, `UPDATE agent_conversations SET model=?,effort=?,revision=? WHERE conversation_id=?`, next.Model, next.Effort, next.Revision, p.ConversationID); err != nil {
			return Outcome{}, err
		}
	} else if err := tx.PutConversationSettings(ctx, next, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	result := map[string]any{"model": next.Model, "effort": next.Effort, "revision": next.Revision}
	return Outcome{Result: result, Events: []Event{{ObjectType: "conversation", ObjectID: store.ConversationID(p.ConversationID), ObjectVersion: next.Revision,
		Kind: "conversation.settings", Body: result}}}, nil
}
