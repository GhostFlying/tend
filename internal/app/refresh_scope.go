package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// requestReviewScope bounds the pre-write review snapshot by the addressed
// responsibility. Global scheduling/configuration and unclassified operations
// retain the full snapshot until their complete write set is explicit.
func requestReviewScope(ctx context.Context, x *Exec, q store.Querier) ([]string, bool, error) {
	ids := map[string]bool{}
	addTask := func(task string) error {
		items, err := store.TaskIssueResponsibilities(ctx, q, task)
		for _, i := range items {
			ids[i.WorkItemID] = true
		}
		return err
	}
	switch x.Env.Target.Type {
	case "issue", "work_item":
		if x.Env.Target.ID != "" {
			ids[x.Env.Target.ID] = true
		}
	case "task":
		if err := addTask(x.Env.Target.ID); err != nil {
			return nil, false, err
		}
	case "run":
		r, err := store.GetRun(ctx, q, x.Env.Target.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		if err := addTask(r.TaskID); err != nil {
			return nil, false, err
		}
	default:
		return nil, true, nil
	}
	var payload struct {
		Parent   string `json:"parent_issue_id"`
		Issue    string `json:"issue_id"`
		WorkItem string `json:"work_item_id"`
	}
	_ = json.Unmarshal(x.Env.Payload, &payload)
	for _, id := range []string{payload.Parent, payload.Issue, payload.WorkItem} {
		if id != "" {
			ids[id] = true
		}
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	return out, false, nil
}

func snapshotReviewedContexts(ctx context.Context, x *Exec, tx *store.Tx) error {
	ids, global, err := requestReviewScope(ctx, x, tx.Q())
	if err != nil {
		return err
	}
	x.globalManagerRefresh = global
	x.reviewedContexts = map[string][]string{}
	x.affectedIssues = map[string]bool{}
	for _, id := range ids {
		x.affectedIssues[id] = true
	}
	if !global && len(ids) == 0 {
		return nil
	}
	query := `SELECT c.work_item_id,r.logical_turn_id FROM work_item_contexts c JOIN logical_turn_context_reviews r ON r.work_item_id=c.work_item_id AND r.revision=c.revision JOIN logical_turns l ON l.logical_turn_id=r.logical_turn_id WHERE l.status='processing'`
	var args []any
	if !global {
		query += ` AND c.work_item_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `)`
		for _, id := range ids {
			args = append(args, id)
		}
	}
	rows, err := tx.Q().QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, logical string
		if err := rows.Scan(&id, &logical); err != nil {
			return err
		}
		x.reviewedContexts[id] = append(x.reviewedContexts[id], logical)
	}
	return rows.Err()
}

func refreshAffectedManagers(ctx context.Context, x *Exec, tx *store.Tx) error {
	if x.globalManagerRefresh && !x.managersRefreshed {
		return refreshManagerTasks(ctx, x, tx)
	}
	for id := range x.affectedIssues {
		if err := refreshIssueManagerTasks(ctx, x, tx, id); err != nil {
			return fmt.Errorf("refresh affected Issue: %w", err)
		}
	}
	return nil
}

func eventIssues(ctx context.Context, q store.Querier, e Event, cache map[string][]protocol.WorkItem) ([]protocol.WorkItem, error) {
	switch e.ObjectType {
	case "work_item", "issue":
		return []protocol.WorkItem{{WorkItemID: e.ObjectID}}, nil
	case "task":
		if items, ok := cache[e.ObjectID]; ok {
			return items, nil
		}
		items, err := store.TaskIssueResponsibilities(ctx, q, e.ObjectID)
		if err == nil {
			cache[e.ObjectID] = items
		}
		return items, err
	}
	return nil, nil
}
