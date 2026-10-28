package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// coordinationReceipt is persisted with the operation, never refreshed on replay.
func coordinationReceipt(ctx context.Context, x *Exec, tx *store.Tx, out *Outcome) error {
	if x.Principal.Kind != protocol.PrincipalMainAgent || !coordinationWrite(x.Env.Operation) {
		return nil
	}
	var turn *protocol.LogicalTurnState
	ids := map[string]bool{}
	request, err := x.reviewExecution(ctx, tx.Q())
	if err != nil {
		return err
	}
	if request == "" && x.Env.Operation == "conversation.commit_turn" && x.Env.Target.Type == "logical_turn" {
		request = x.Env.Target.ID
	}
	if request != "" {
		v, err := store.LogicalTurn(ctx, tx.Q(), request)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err == nil {
			turn = &v
			for _, c := range v.Contexts {
				ids[c.WorkItemID] = true
			}
		}
	}
	if x.Env.Target.Type == "work_item" && x.Env.Target.ID != "" {
		ids[x.Env.Target.ID] = true
	}
	for _, e := range out.Events {
		if e.ObjectType == "work_item" {
			ids[e.ObjectID] = true
		}
	}
	contexts := []map[string]any{}
	pending := []map[string]any{}
	if turn != nil {
		for _, m := range turn.Messages {
			if m.Disposition == "pending" || !m.FactsReviewed {
				pending = append(pending, map[string]any{"kind": "message_review", "message_id": m.MessageID, "content_revision": m.ContentRevision, "association_revision": m.Revision})
			}
		}
	}
	keys := make([]string, 0, len(ids))
	for id := range ids {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, id := range keys {
		w, err := store.GetIssue(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		var revision int64
		if err := tx.Q().QueryRowContext(ctx, `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, id).Scan(&revision); err != nil {
			return err
		}
		ready, err := store.WorkItemContextReady(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		blockers, err := store.ContextBlockers(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		bindings, err := store.TaskContextBlockers(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		blockers = append(blockers, bindings...)
		ready = ready && len(bindings) == 0
		if !ready {
			pending = append(pending, map[string]any{"kind": "dispatch_blocked", "work_item_id": id, "reasons": blockers})
		}
		reviewed := true
		if turn != nil {
			if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM logical_turn_context_reviews WHERE logical_turn_id=? AND work_item_id=? AND revision=?)`, turn.ID, id, revision).Scan(&reviewed); err != nil {
				return err
			}
		}
		if !reviewed {
			pending = append(pending, map[string]any{"kind": "context_review", "work_item_id": id, "revision": revision})
		}
		contexts = append(contexts, map[string]any{"work_item_id": id, "work_item_revision": w.Revision, "context_revision": revision, "dispatch_ready": ready, "reviewed": reviewed})
	}
	raw, err := json.Marshal(out.Result)
	if err != nil {
		return err
	}
	result := map[string]any{}
	if string(raw) != "null" {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&result); err != nil {
			return err
		}
	}
	result["receipt"] = map[string]any{"turn": turn, "contexts": contexts, "pending": pending}
	out.Result = result
	return nil
}
func coordinationWrite(op string) bool {
	return strings.HasPrefix(op, "work_items.") || strings.HasPrefix(op, "work_item_context.") || strings.HasPrefix(op, "tasks.") || strings.HasPrefix(op, "work_item_choices.") || strings.HasPrefix(op, "decisions.") || op == "control.set_mode" || op == "actions.create" || op == "conversation.set_associations" || op == "conversation.commit_turn" || op == "conversation.reply"
}
