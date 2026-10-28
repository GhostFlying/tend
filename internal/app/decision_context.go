package app

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// reviewAppliedOwnerChoice reviews only a newly recorded, exact panel choice
// whose declared effect was applied under its decision and task revisions.
// It never interprets free text or grants authority for a future action.
func reviewAppliedOwnerChoice(ctx context.Context, x *Exec, tx *store.Tx, d store.DecisionRow, p protocol.AnswerDecisionPayload, effect protocol.EffectResult) error {
	task, err := store.GetTask(ctx, tx.Q(), d.TaskID)
	if err != nil {
		return err
	}
	acceptGeneration, err := store.LatestAcceptGeneration(ctx, tx.Q(), task.TaskID, task.SpecVersion, task.CurrentArtifactID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"decision_id": d.DecisionID, "decision_version": d.Version, "task_id": d.TaskID, "spec_version": task.SpecVersion, "asked_control_revision": d.AskedRevision.Int64, "applied_control_revision": task.ControlRevision, "option": p.Option, "effect": effect.Effect, "implement_generation": task.ImplementGeneration, "accept_generation": acceptGeneration, "request_id": x.Env.RequestID, "scope": protocol.AppliedTransitionAuthorizationScope})
	if err != nil {
		return err
	}
	associations, err := store.MessageAssociations(ctx, tx.Q(), p.SourceMessageID)
	if err != nil {
		return err
	}
	for _, item := range associations.Items {
		if _, err := tx.Exec(ctx, `INSERT INTO work_item_context_facts(fact_id,work_item_id,scope,kind,body,source_message_id,attribution,created_at) VALUES(?,?,'work_item','authorization',?,?,'owner',?)`, x.IDs.New(protocol.Prefix("fact")), item.WorkItemID, string(body), p.SourceMessageID, x.Now.UnixMilli()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE work_item_contexts SET revision=revision+1 WHERE work_item_id=?`, item.WorkItemID); err != nil {
			return err
		}
	}
	disposition := "unassigned"
	if len(associations.Items) > 0 {
		disposition = "linked"
	}
	if _, err := tx.Exec(ctx, `UPDATE message_dispositions SET disposition=?,classified_revision=content_revision,facts_revision=content_revision,source='program',updated_at=? WHERE message_id=? AND content_revision=?`, disposition, x.Now.UnixMilli(), p.SourceMessageID, associations.ContentRevision); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE message_context_obligations SET needs_review=0 WHERE message_id=?`, p.SourceMessageID); err != nil {
		return err
	}
	actor, err := json.Marshal(x.Principal)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO message_disposition_history(message_id,content_revision,association_revision,disposition,actor,created_at) VALUES(?,?,?,?,?,?)`, p.SourceMessageID, associations.ContentRevision, associations.Revision, disposition, string(actor), x.Now.UnixMilli())
	return err
}
