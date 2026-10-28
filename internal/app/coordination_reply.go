package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// CoordinationReply publishes and reviews one formal message in one transaction.
type CoordinationReply struct{}

func (CoordinationReply) Operation() string { return "conversation.reply" }
func (CoordinationReply) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalMainAgent)
}
func (CoordinationReply) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.CoordinationReply
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.Purpose != "progress" && p.Purpose != "final" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "purpose must be progress or final")
	}
	if len(p.WorkItemIDs) > 50 || len(p.Facts) > 100 || len(p.Contexts) != len(p.WorkItemIDs) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "declare each linked context version")
	}
	if err := checkFactKinds(p.ReviewedKinds); err != nil {
		return Outcome{}, err
	}
	execution, err := x.reviewExecution(ctx, tx.Q())
	if err != nil {
		return Outcome{}, err
	}
	turn, err := store.GetMainTurn(ctx, tx.Q(), execution)
	if err != nil {
		return Outcome{}, err
	}
	if turn.ThreadID != x.Principal.Thread || turn.Status != store.TurnStarted {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "reply requires this thread's admitted active turn")
	}
	var repair bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT repair FROM logical_turn_executions WHERE request_id=?`, turn.RequestID).Scan(&repair); err != nil {
		return Outcome{}, err
	}
	if repair {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "repair cannot publish business replies")
	}
	expected := map[string]int64{}
	for _, c := range p.Contexts {
		if _, ok := expected[c.WorkItemID]; ok || !slices.Contains(p.WorkItemIDs, c.WorkItemID) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "declare distinct linked context versions")
		}
		var revision int64
		if err := tx.Q().QueryRowContext(ctx, `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, c.WorkItemID).Scan(&revision); err != nil {
			return Outcome{}, err
		}
		if revision != c.Revision {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "reply context changed")
		}
		expected[c.WorkItemID] = revision
	}
	child := *x
	env := *x.Env
	child.Env = &env
	sources, err := store.FollowupIDs(ctx, tx.Q(), `SELECT message_id FROM conversation_messages WHERE message_id=? OR message_id IN (SELECT message_id FROM main_steers WHERE thread_id=? AND turn_id=? AND native_item_id<>'') ORDER BY seq`, turn.MessageID, turn.ThreadID, turn.TurnID)
	if err != nil {
		return Outcome{}, err
	}
	replyTo := turn.MessageID
	if len(sources) > 0 {
		replyTo = sources[len(sources)-1]
	}
	env.Payload, _ = json.Marshal(recordAgentMessagePayload{Body: p.Body, ReplyTo: replyTo, ReplySources: sources, TurnID: turn.TurnID, ItemID: "reply:" + x.Env.RequestID})
	// This is explicit publication; ordinary native background prose may buffer.
	out, err := recordVisibleAgentMessage(ctx, &child, tx)
	if err != nil {
		return Outcome{}, err
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		return Outcome{}, fmt.Errorf("app: formal publication returned no message result")
	}
	id, ok := result["message_id"].(string)
	if !ok || id == "" {
		return Outcome{}, fmt.Errorf("app: formal publication returned no message identity")
	}
	assoc, err := store.MessageAssociations(ctx, tx.Q(), id)
	if err != nil {
		return Outcome{}, err
	}
	env.Target = protocol.Target{Type: "message", ID: id}
	env.ExpectedControlRevision = &assoc.Revision
	env.Payload, _ = json.Marshal(protocol.SetMessageAssociationsPayload{Disposition: p.Disposition, WorkItemIDs: p.WorkItemIDs, ContentRevision: assoc.ContentRevision})
	linked, err := (SetMessageAssociations{}).Apply(ctx, &child, tx)
	if err != nil {
		return Outcome{}, err
	}
	out.Events = append(out.Events, linked.Events...)
	for _, fact := range p.Facts {
		if _, ok := expected[fact.WorkItemID]; !ok || fact.Scope != "work_item" || (fact.SourceMessageID != "" && fact.SourceMessageID != id) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "reply facts must name a linked work item and this message")
		}
		fact.SourceMessageID = id
		env.Target = protocol.Target{Type: "work_item", ID: fact.WorkItemID}
		var revision int64
		if err := tx.Q().QueryRowContext(ctx, `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, fact.WorkItemID).Scan(&revision); err != nil {
			return Outcome{}, err
		}
		env.ExpectedControlRevision = &revision
		env.Payload, _ = json.Marshal(fact)
		added, err := (ContextCommand{Action: "fact"}).Apply(ctx, &child, tx)
		if err != nil {
			return Outcome{}, err
		}
		out.Events = append(out.Events, added.Events...)
	}
	assoc, err = store.MessageAssociations(ctx, tx.Q(), id)
	if err != nil {
		return Outcome{}, err
	}
	review := protocol.LogicalTurnCommit{Messages: []protocol.LogicalTurnReview{{MessageID: id, ContentRevision: assoc.ContentRevision, AssociationRevision: assoc.Revision}}}
	for _, c := range p.Contexts {
		if err := tx.Q().QueryRowContext(ctx, `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, c.WorkItemID).Scan(&c.Revision); err != nil {
			return Outcome{}, err
		}
		review.Contexts = append(review.Contexts, c)
	}
	env.Payload, _ = json.Marshal(review)
	checked, err := (CommitLogicalTurn{RequestID: turn.RequestID}).Apply(ctx, &child, tx)
	if err != nil {
		return Outcome{}, err
	}
	out.Events = append(out.Events, checked.Events...)
	batch, batchErr := store.BackgroundByMessage(ctx, tx.Q(), turn.MessageID)
	if batchErr != nil && !errors.Is(batchErr, store.ErrNotFound) {
		return Outcome{}, batchErr
	}
	if batchErr == nil {
		// A committed formal reply supersedes an earlier silent choice. The
		// request/message identity also fences later check-ins in this turn.
		if _, err := tx.Exec(ctx, `UPDATE background_batches SET checkin_json='' WHERE batch_id=?`, batch.BatchID); err != nil {
			return Outcome{}, err
		}
		result["background_batch_id"] = batch.BatchID
	}
	out.Notify = true
	result["purpose"] = p.Purpose
	return out, nil
}

func checkFactKinds(kinds []string) error {
	for _, kind := range []string{"goal", "constraint", "authorization", "refusal", "commitment", "question", "suggestion"} {
		if !slices.Contains(kinds, kind) {
			return protocol.Errorf(protocol.CodeInvalidRequest, "reviewed_kinds must include all seven kinds; missing %s", kind)
		}
	}
	return nil
}
