package app

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type InjectSource struct{}

func (InjectSource) Operation() string { return "conversation.inject_source" }
func (InjectSource) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (InjectSource) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.InjectSourcePayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if !utf8.ValidString(p.Text) || len(p.Text) > maxMessageBytes || len(p.Metadata.Author) > 2000 || len(p.Metadata.URL) > 8192 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "source text or metadata is invalid or too large")
	}
	if strings.TrimSpace(p.Text) == "" && len(p.Attachments) == 0 && p.Metadata.URL == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "source text, original file or link is required")
	}
	if p.Metadata.CorrectionOf != "" {
		if _, err := store.ManualSource(ctx, tx.Q(), p.Metadata.CorrectionOf); err != nil {
			return Outcome{}, errNotFound(err, "original source does not exist")
		}
	}
	body := p.Text
	if body == "" && len(p.Attachments) == 0 {
		body = p.Metadata.URL
	}
	adapted := *x
	env := *x.Env
	adapted.Env = &env
	env.Payload, _ = json.Marshal(protocol.SubmitMessagePayload{ConversationID: p.ConversationID, Body: body, Attachments: p.Attachments})
	out, err := (SubmitMessage{externalSource: true}).Apply(ctx, &adapted, tx)
	if err != nil {
		return Outcome{}, err
	}
	result := out.Result.(map[string]any)
	id := result["message_id"].(string)
	value := protocol.ManualSource{MessageID: id, ConversationID: store.ConversationID(p.ConversationID), Metadata: p.Metadata, SuppliedBy: x.Principal}
	raw, _ := json.Marshal(value)
	if _, err := tx.Exec(ctx, `INSERT INTO manual_message_sources(message_id,metadata_json) VALUES(?,?)`, id, string(raw)); err != nil {
		return Outcome{}, err
	}
	// External evidence does not impersonate an owner instruction, but an
	// explicit Issue injection still creates that manager's source-review duty.
	conversation, err := store.AgentConversation(ctx, tx.Q(), value.ConversationID)
	if err != nil {
		return Outcome{}, err
	}
	if conversation.WorkItemID != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO message_context_obligations(message_id,work_item_id,needs_review) VALUES(?,?,1) ON CONFLICT(message_id,work_item_id) DO UPDATE SET needs_review=1`, id, conversation.WorkItemID); err != nil {
			return Outcome{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE work_item_contexts SET revision=revision+1 WHERE work_item_id=?`, conversation.WorkItemID); err != nil {
			return Outcome{}, err
		}
	}
	value, err = store.ManualSource(ctx, tx.Q(), id)
	if err != nil {
		return Outcome{}, err
	}
	out.Result = value
	return out, nil
}
func (a *App) ManualSource(ctx context.Context, id string) (protocol.ManualSource, error) {
	var value protocol.ManualSource
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		value, err = store.ManualSource(ctx, tx.Q(), id)
		return err
	})
	return value, err
}
