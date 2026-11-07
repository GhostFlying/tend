package views

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/store"
)

const usageRunForTurnSQL = `SELECT COUNT(DISTINCT run_id),COALESCE(MIN(run_id),'') FROM run_logs WHERE kind IN ('native.turn_started','native.turn_completed','item.completed') AND json_extract(body_json,'$.thread_id')=? AND json_extract(body_json,'$.turn_id')=?`
const usageActorSQL = `SELECT COALESCE(json_extract(source_json,'$.actor_name'),''),COALESCE(json_extract(source_json,'$.actor_id'),''),COALESCE(json_extract(source_json,'$.actor_categories'),'[]') FROM token_usage_local_sources WHERE conversation_id=? AND (json_extract(source_json,'$.thread_id')=? OR json_extract(source_json,'$.thread_id')=? OR json_extract(source_json,'$.session_id')=?) ORDER BY (json_extract(source_json,'$.thread_id')=?) DESC LIMIT 1`

type usageTurnKey struct{ thread, turn string }
type usageActorValue struct{ name, id, categories string }
type usageActorKey struct{ conversation, thread, parent, session string }
type usageActorResult struct {
	value usageActorValue
	found bool
}

// usageResolver caches indexed lookups within one existing read transaction.
// Missing/ambiguous attribution is a fact; a failed query is never that fact.
type usageResolver struct {
	q            store.Querier
	runResults   map[usageTurnKey]string
	actorResults map[usageActorKey]usageActorResult
}

func newUsageResolver(q store.Querier) *usageResolver {
	return &usageResolver{q: q, runResults: map[usageTurnKey]string{}, actorResults: map[usageActorKey]usageActorResult{}}
}

func (r *usageResolver) runForTurn(ctx context.Context, thread, turn string) (string, error) {
	key := usageTurnKey{thread, turn}
	if old, ok := r.runResults[key]; ok {
		return old, nil
	}
	var count int
	var run string
	if err := r.q.QueryRowContext(ctx, usageRunForTurnSQL, thread, turn).Scan(&count, &run); err != nil {
		return "", fmt.Errorf("resolve usage run: %w", err)
	}
	if count != 1 {
		run = ""
	}
	r.runResults[key] = run
	return run, nil
}

func (r *usageResolver) actor(ctx context.Context, conversation, thread, parent, session string) (usageActorValue, bool, error) {
	key := usageActorKey{conversation, thread, parent, session}
	if old, ok := r.actorResults[key]; ok {
		return old.value, old.found, nil
	}
	var value usageActorValue
	found, err := scanUsageRow(r.q.QueryRowContext(ctx, usageActorSQL, conversation, thread, parent, session, thread), &value.name, &value.id, &value.categories)
	if err != nil {
		return value, false, fmt.Errorf("resolve usage actor: %w", err)
	}
	r.actorResults[key] = usageActorResult{value, found}
	return value, found, nil
}

func scanUsageRow(row *sql.Row, values ...any) (bool, error) {
	err := row.Scan(values...)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
