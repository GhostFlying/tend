package views

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

type usageResolverQueries struct {
	store.Querier
	batches, singles map[string]int
}

func (q *usageResolverQueries) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	q.batches[query]++
	return q.Querier.QueryContext(ctx, query, args...)
}

func (q *usageResolverQueries) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	q.singles[query]++
	return q.Querier.QueryRowContext(ctx, query, args...)
}

func newUsageResolverStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := viewFixture.Open(context.Background(), filepath.Join(t.TempDir(), "ledger.db"), testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func usageResolverWrite(t *testing.T, s *store.Store, f func(*store.Tx) error) {
	t.Helper()
	if err := s.Write(context.Background(), f); err != nil {
		t.Fatal(err)
	}
}

func TestUsageResolverActorsMatchOriginalSQL(t *testing.T) {
	ctx := context.Background()
	s := newUsageResolverStore(t)
	usageResolverWrite(t, s, func(tx *store.Tx) error {
		for i, raw := range []string{
			`{"thread_id":"parent","session_id":"other","actor_name":"parent","actor_id":"p","actor_categories":["p"]}`,
			`{"thread_id":"elsewhere","session_id":"session","actor_name":"session","actor_id":"s","actor_categories":["s"]}`,
			`{"thread_id":"direct","session_id":"other","actor_name":"direct","actor_id":"d","actor_categories":["d"]}`,
			`{"thread_id":null,"session_id":"rank","actor_name":"null-rank"}`,
			`{"thread_id":"different","session_id":"rank","actor_name":"false-rank"}`,
			`{"session_id":"null-only","actor_name":null,"actor_id":null,"actor_categories":null}`,
			`{"thread_id":"tie","actor_name":"first"}`,
			`{"thread_id":"tie","actor_name":"second"}`,
			`{"thread_id":"same","actor_name":"same","actor_categories":["same"]}`,
			`{"thread_id":"same","actor_name":"same","actor_categories":["same"]}`,
			`{"thread_id":12,"session_id":13,"actor_name":"number"}`,
			`{"thread_id":"","session_id":"","actor_name":"empty"}`,
			`{"thread_id":["array"],"actor_name":"array","actor_categories":["a"]}`,
		} {
			if _, err := tx.Exec(ctx, `INSERT INTO token_usage_local_sources(source_key,conversation_id,source_json,coverage,updated_at) VALUES(?,?,?,'caught_up',1)`, fmt.Sprint(i), "conversation", raw); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO token_usage_local_sources(source_key,conversation_id,source_json,coverage,updated_at) VALUES('other-conversation','other','{"thread_id":"direct","actor_name":"wrong-domain"}','caught_up',1)`)
		return err
	})
	if err := s.Read(ctx, func(tx *store.ReadTx) error {
		q := &usageResolverQueries{Querier: tx.Q(), batches: map[string]int{}, singles: map[string]int{}}
		r := newUsageResolver(q)
		for _, key := range []usageActorKey{
			{"conversation", "direct", "parent", "session"},
			{"conversation", "absent", "parent", "absent"},
			{"conversation", "absent", "absent", "session"},
			{"conversation", "absent", "absent", "rank"},
			{"conversation", "absent", "absent", "null-only"},
			{"conversation", "tie", "absent", "absent"},
			{"conversation", "same", "absent", "absent"},
			{"conversation", "12", "absent", "13"},
			{"conversation", "", "absent", "absent"},
			{"conversation", `["array"]`, "absent", "absent"},
			{"missing", "direct", "parent", "session"},
		} {
			var want usageActorValue
			err := tx.Q().QueryRowContext(ctx, usageActorSQL, key.conversation, key.thread, key.parent, key.session, key.thread).Scan(&want.name, &want.id, &want.categories)
			for range 2 {
				got, found, lookupErr := r.actor(ctx, key.conversation, key.thread, key.parent, key.session)
				if lookupErr != nil || found != (err == nil) || got != want {
					t.Fatalf("actor(%+v) = %+v,%v; original = %+v,%v", key, got, found, want, err)
				}
			}
		}
		if q.singles[usageActorSQL] != 11 || len(q.batches) != 0 {
			t.Fatalf("expected one indexed query per key and no batch reads: batches=%v singles=%v", q.batches, q.singles)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUsageResolverRunAmbiguityMatchesOriginalSQL(t *testing.T) {
	ctx := context.Background()
	s := newUsageResolverStore(t)
	keys := []usageTurnKey{{"thread", "unique"}, {"thread", "ambiguous"}, {"thread", "ignored"}, {"", ""}, {"12", "13"}, {`["array"]`, "array"}}
	usageResolverWrite(t, s, func(tx *store.Tx) error {
		for _, query := range []string{
			`INSERT INTO tasks(task_id,title,lifecycle,control_mode,control_revision,spec_version,priority,block_reason,created_at,updated_at) VALUES('task','fixture','draft','managed',1,1,0,'none',1,1)`,
			`INSERT INTO plan_actions(action_id,task_id,kind,effect_key,trigger_json,target_spec_version,authorization_json,generation,status,created_request_id,created_at,updated_at) VALUES('action','task','implement','fixture','{}',1,'{}',1,'done','request',1,1)`,
		} {
			if _, err := tx.Exec(ctx, query); err != nil {
				return err
			}
		}
		for i, id := range []string{"a", "b"} {
			if _, err := tx.Exec(ctx, `INSERT INTO runs(run_id,task_id,action_id,generation,role,desired_state,observed_state,created_at,updated_at) VALUES(?,'task','action',?,'implement','run','finished',1,1)`, id, i+1); err != nil {
				return err
			}
		}
		for i, key := range keys {
			seq, err := store.InsertUsageFact(ctx, tx, protocol.TokenUsageSample{Source: "fixture", RequestID: fmt.Sprint(i), ThreadID: key.thread, TurnID: key.turn, Revision: 1}, "conversation:main", "main", 1)
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO token_usage_current(source,request_id,fact_seq,conflict) VALUES('fixture',?,?,0)`, fmt.Sprint(i), seq); err != nil {
				return err
			}
		}
		for i, row := range []struct{ run, kind, body string }{
			{"a", "item.completed", `{"thread_id":"thread","turn_id":"unique"}`},
			{"a", "native.turn_completed", `{"thread_id":"thread","turn_id":"unique"}`},
			{"a", "native.turn_started", `{"thread_id":"thread","turn_id":"ambiguous"}`},
			{"b", "item.completed", `{"thread_id":"thread","turn_id":"ambiguous"}`},
			{"a", "unrelated", `{"thread_id":"thread","turn_id":"ignored"}`},
			{"a", "item.completed", `{"thread_id":null,"turn_id":null}`},
			{"b", "item.completed", `{"thread_id":"","turn_id":""}`},
			{"a", "item.completed", `{"thread_id":12,"turn_id":13}`},
			{"a", "item.completed", `{"thread_id":["array"],"turn_id":"array"}`},
			{"a", "item.completed", `{"thread_id":"unneeded","turn_id":"unneeded"}`},
		} {
			if _, err := tx.Exec(ctx, `INSERT INTO run_logs(run_id,seq,kind,body_json,created_at) VALUES(?,?,?,?,1)`, row.run, i+1, row.kind, row.body); err != nil {
				return err
			}
		}
		return nil
	})
	if err := s.Read(ctx, func(tx *store.ReadTx) error {
		q := &usageResolverQueries{Querier: tx.Q(), batches: map[string]int{}, singles: map[string]int{}}
		r := newUsageResolver(q)
		for _, key := range keys {
			var count int
			var want string
			if err := tx.Q().QueryRowContext(ctx, usageRunForTurnSQL, key.thread, key.turn).Scan(&count, &want); err != nil {
				return err
			}
			if count != 1 {
				want = ""
			}
			if got, err := r.runForTurn(ctx, key.thread, key.turn); err != nil || got != want {
				t.Fatalf("run(%+v)=%q; original=%q", key, got, want)
			}
		}
		if len(r.runResults) != len(keys) || len(q.batches) != 0 || q.singles[usageRunForTurnSQL] != len(keys) {
			t.Fatalf("expected indexed per-key reads only: runs=%v queries=%v", r.runResults, q)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUsageResolverDoesNotCacheAcrossRequests(t *testing.T) {
	ctx := context.Background()
	s := newUsageResolverStore(t)
	read := func(want string) {
		t.Helper()
		if err := s.Read(ctx, func(tx *store.ReadTx) error {
			r := newUsageResolver(tx.Q())
			actor, found, err := r.actor(ctx, "conversation", "thread", "parent", "session")
			if err != nil || found != (want != "") || actor.name != want {
				t.Fatalf("actor=%+v,%v want %q", actor, found, want)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	read("")
	for _, actor := range []string{"first", "second"} {
		usageResolverWrite(t, s, func(tx *store.Tx) error {
			payload, err := json.Marshal(map[string]string{"thread_id": "thread", "actor_name": actor})
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO token_usage_local_sources(source_key,conversation_id,source_json,coverage,updated_at) VALUES('source','conversation',?,'caught_up',1) ON CONFLICT(source_key) DO UPDATE SET source_json=excluded.source_json`, string(payload))
			return err
		})
		read(actor)
	}
}

func TestUsageResolverActorErrorDoesNotCacheFailure(t *testing.T) {
	// A failed lookup must not prevent a later indexed query from succeeding.
	// QueryRow cannot return an error directly, so use a cancelled context for
	// the first lookup against the real driver rather than synthesizing a Row.
	ctx := context.Background()
	s := newUsageResolverStore(t)
	usageResolverWrite(t, s, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO token_usage_local_sources(source_key,conversation_id,source_json,coverage,updated_at) VALUES('source','conversation','{"thread_id":"thread","actor_name":"actor"}','caught_up',1)`)
		return err
	})
	if err := s.Read(ctx, func(tx *store.ReadTx) error {
		q := &usageResolverQueries{Querier: tx.Q(), batches: map[string]int{}, singles: map[string]int{}}
		r := newUsageResolver(q)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, found, err := r.actor(cancelled, "conversation", "thread", "parent", "session"); found || !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled lookup returned an actor")
		}
		got, found, err := r.actor(ctx, "conversation", "thread", "parent", "session")
		if err != nil || !found || got.name != "actor" || len(r.actorResults) != 1 {
			t.Fatalf("failed lookup cached: %+v,%v", got, found)
		}
		if len(q.batches) != 0 || q.singles[usageActorSQL] != 2 {
			t.Fatal("failed query was cached or used a batch read", q)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
