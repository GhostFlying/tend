package views

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GhostFlying/tend/internal/store"
)

const usageRunForTurnSQL = `SELECT COUNT(DISTINCT run_id),COALESCE(MIN(run_id),'') FROM run_logs WHERE kind IN ('native.turn_started','native.turn_completed','item.completed') AND json_extract(body_json,'$.thread_id')=? AND json_extract(body_json,'$.turn_id')=?`
const usageActorSQL = `SELECT COALESCE(json_extract(source_json,'$.actor_name'),''),COALESCE(json_extract(source_json,'$.actor_id'),''),COALESCE(json_extract(source_json,'$.actor_categories'),'[]') FROM token_usage_local_sources WHERE conversation_id=? AND (json_extract(source_json,'$.thread_id')=? OR json_extract(source_json,'$.thread_id')=? OR json_extract(source_json,'$.session_id')=?) ORDER BY (json_extract(source_json,'$.thread_id')=?) DESC LIMIT 1`

type usageTurnKey struct{ thread, turn string }
type usageRunChoice struct {
	run              string
	found, ambiguous bool
}
type usageActorValue struct{ name, id, categories string }
type usageActorChoice struct {
	value            usageActorValue
	found, ambiguous bool
}
type usageSourceKey struct{ conversation, native string }
type usageActorKey struct{ conversation, thread, parent, session string }
type usageActorResult struct {
	value usageActorValue
	found bool
}

// usageResolver belongs to exactly one view and its existing read transaction.
// It never saves a result across requests or changes attribution facts.
type usageResolver struct {
	q                                        store.Querier
	runsLoaded                               bool
	runs                                     map[usageTurnKey]usageRunChoice
	actorsLoaded                             bool
	byThread, bySession, byNullThreadSession map[usageSourceKey]usageActorChoice
	actorResults                             map[usageActorKey]usageActorResult
}

func newUsageResolver(q store.Querier) *usageResolver {
	return &usageResolver{q: q, actorResults: map[usageActorKey]usageActorResult{}}
}

func (r *usageResolver) runForTurn(ctx context.Context, thread, turn string) string {
	if !r.runsLoaded {
		r.runsLoaded = true
		r.runs = r.loadRuns(ctx)
	}
	if r.runs != nil {
		choice := r.runs[usageTurnKey{thread, turn}]
		if !choice.ambiguous {
			return choice.run
		}
		return ""
	}
	var count int
	var run string
	if r.q.QueryRowContext(ctx, usageRunForTurnSQL, thread, turn).Scan(&count, &run) == nil && count == 1 {
		return run
	}
	return ""
}

func (r *usageResolver) loadRuns(ctx context.Context) map[usageTurnKey]usageRunChoice {
	// Keep only keys present in current usage facts. A historical log scan must
	// not retain every turn ever seen by the service in request memory.
	wanted := map[usageTurnKey]usageRunChoice{}
	rows, err := r.q.QueryContext(ctx, `SELECT COALESCE(json_extract(f.payload_json,'$.thread_id'),''),COALESCE(json_extract(f.payload_json,'$.turn_id'),'') FROM token_usage_current c JOIN token_usage_facts f ON f.seq=c.fact_seq`)
	if err != nil {
		return nil
	}
	for rows.Next() {
		var thread, turn any
		if err = rows.Scan(&thread, &turn); err != nil {
			break
		}
		t, tok := thread.(string)
		u, uok := turn.(string)
		if tok && uok {
			wanted[usageTurnKey{t, u}] = usageRunChoice{}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	closeErr := rows.Close()
	if err != nil || closeErr != nil {
		return nil
	}
	rows, err = r.q.QueryContext(ctx, `SELECT json_extract(body_json,'$.thread_id'),json_extract(body_json,'$.turn_id'),run_id FROM run_logs WHERE kind IN ('native.turn_started','native.turn_completed','item.completed')`)
	if err != nil {
		return nil
	}
	for rows.Next() {
		var thread, turn any
		var run string
		if err = rows.Scan(&thread, &turn, &run); err != nil {
			break
		}
		t, tok := thread.(string)
		u, uok := turn.(string)
		if !tok || !uok {
			continue
		}
		key := usageTurnKey{t, u}
		choice, needed := wanted[key]
		if !needed {
			continue
		}
		if !choice.found {
			choice.run, choice.found = run, true
		} else if choice.run != run {
			choice.ambiguous = true
		}
		wanted[key] = choice
	}
	if err == nil {
		err = rows.Err()
	}
	closeErr = rows.Close()
	if err != nil || closeErr != nil {
		return nil
	}
	return wanted
}

func mergeUsageActor(a, b usageActorChoice) usageActorChoice {
	if !a.found {
		return b
	}
	if !b.found {
		return a
	}
	a.ambiguous = a.ambiguous || b.ambiguous || a.value != b.value
	return a
}

func addUsageActor(index map[usageSourceKey]usageActorChoice, key usageSourceKey, value usageActorValue) {
	index[key] = mergeUsageActor(index[key], usageActorChoice{value: value, found: true})
}

func (r *usageResolver) loadActors(ctx context.Context) bool {
	byThread := map[usageSourceKey]usageActorChoice{}
	bySession := map[usageSourceKey]usageActorChoice{}
	byNull := map[usageSourceKey]usageActorChoice{}
	// Project with the original SQLite expressions so JSON scalar conversion,
	// NULL handling and actor categories do not acquire Go JSON semantics.
	rows, err := r.q.QueryContext(ctx, `SELECT conversation_id,json_extract(source_json,'$.thread_id'),json_extract(source_json,'$.session_id'),COALESCE(json_extract(source_json,'$.actor_name'),''),COALESCE(json_extract(source_json,'$.actor_id'),''),COALESCE(json_extract(source_json,'$.actor_categories'),'[]') FROM token_usage_local_sources`)
	if err != nil {
		return false
	}
	for rows.Next() {
		var conversation string
		var thread, session any
		var value usageActorValue
		if err = rows.Scan(&conversation, &thread, &session, &value.name, &value.id, &value.categories); err != nil {
			break
		}
		if t, ok := thread.(string); ok {
			addUsageActor(byThread, usageSourceKey{conversation, t}, value)
		}
		if s, ok := session.(string); ok {
			if thread == nil {
				addUsageActor(byNull, usageSourceKey{conversation, s}, value)
			} else {
				addUsageActor(bySession, usageSourceKey{conversation, s}, value)
			}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	closeErr := rows.Close()
	if err != nil || closeErr != nil {
		return false
	}
	r.byThread, r.bySession, r.byNullThreadSession = byThread, bySession, byNull
	return true
}

func (r *usageResolver) actor(ctx context.Context, conversation, thread, parent, session string) (usageActorValue, bool) {
	key := usageActorKey{conversation, thread, parent, session}
	if old, ok := r.actorResults[key]; ok {
		return old.value, old.found
	}
	if !r.actorsLoaded {
		r.actorsLoaded = true
		r.loadActors(ctx)
	}
	if r.byThread != nil {
		// SQLite DESC ranks a true direct match above false, and false above NULL.
		choice := r.byThread[usageSourceKey{conversation, thread}]
		if !choice.found {
			choice = mergeUsageActor(r.byThread[usageSourceKey{conversation, parent}], r.bySession[usageSourceKey{conversation, session}])
		}
		if !choice.found {
			choice = r.byNullThreadSession[usageSourceKey{conversation, session}]
		}
		if !choice.ambiguous {
			result := usageActorResult{choice.value, choice.found}
			r.actorResults[key] = result
			return result.value, result.found
		}
	}
	// Conflicting highest-ranked projections deliberately retain the original
	// SQL tie selection. Cache that result only for these exact query arguments.
	var value usageActorValue
	err := r.q.QueryRowContext(ctx, usageActorSQL, conversation, thread, parent, session, thread).Scan(&value.name, &value.id, &value.categories)
	if err == nil || errors.Is(err, sql.ErrNoRows) {
		r.actorResults[key] = usageActorResult{value, err == nil}
	}
	return value, err == nil
}
