package app

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type concurrencySampleStep struct {
	source protocol.ConcurrencySource
	state  string
	seen   int64
}

func TestConcurrencySyncSamplingMatchesEachTransition(t *testing.T) {
	native := func(id, env, parent string) protocol.ConcurrencySource {
		return protocol.ConcurrencySource{ExecutionID: id, EnvironmentID: env, RuntimeID: "worker", AgentID: "agent", SessionID: id, NativeSessionID: "native-" + id, EventID: "event-" + id, ParentExecutionID: parent}
	}
	for _, scenario := range []string{"health_reset", "cross_group_parent", "rebound_stop", "heartbeat_fresh", "heartbeat_expired", "heartbeat_freshness_boundary", "heartbeat_unknown"} {
		t.Run(scenario, func(t *testing.T) {
			var expected [][]string
			for _, cached := range []bool{false, true} {
				h := newHarness(t)
				for _, group := range []string{"a", "b"} {
					concurrencyConfigure(t, h, "create", group, 0, protocol.ConfigureConcurrencyPayload{Name: group, Min: 1, Initial: 2, Max: 4})
				}
				now := h.clock.Now().UnixMilli()
				var snapshots [][]string
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					if _, err := tx.Exec(t.Context(), `INSERT INTO concurrency_bindings VALUES('env-a','worker','a'),('env-b','worker','b')`); err != nil {
						return err
					}
					parent := native("parent", "env-a", "")
					other := native("other", "env-a", "")
					child := native("child", "env-b", "")
					initialParent := "running"
					if scenario == "cross_group_parent" || scenario == "heartbeat_unknown" {
						initialParent = "unknown"
					}
					for _, step := range []concurrencySampleStep{{parent, initialParent, now}, {other, "running", now}, {child, "stopped", now}} {
						if err := putConcurrencyExecutionObserved(t.Context(), tx, step.source, step.state, step.seen, now); err != nil {
							return err
						}
					}
					// Give the reference a real accumulated proof that an unhealthy
					// intermediate transition must invalidate, even at the same time.
					s, err := concurrencyState(t.Context(), tx.Q(), "a")
					if err != nil {
						return err
					}
					s.LastSample = now - 1000
					s.LastHealthy, s.LastRunning = true, 2
					s.Group.ObservationSince = now - time.Minute.Milliseconds()
					s.Intervals = []domain.ConcurrencyInterval{{Start: now - 2000, End: now - 1000}}
					s.Successes = []int64{now - 500}
					if err := saveConcurrency(t.Context(), tx, s); err != nil {
						return err
					}
					steps := []concurrencySampleStep{{parent, "running", now}, {parent, "running", now - 1000}, {parent, "unknown", now}, {parent, "running", now}}
					switch scenario {
					case "heartbeat_fresh":
						now += time.Second.Milliseconds()
						steps = []concurrencySampleStep{{parent, "running", now}, {other, "running", now}, {parent, "running", now - 500}}
					case "heartbeat_expired":
						now += RunnerOfflineAfter.Milliseconds() + 2
						steps = []concurrencySampleStep{{parent, "running", now - RunnerOfflineAfter.Milliseconds() - 1}, {other, "running", now - RunnerOfflineAfter.Milliseconds() - 1}}
					case "heartbeat_freshness_boundary":
						now += RunnerOfflineAfter.Milliseconds() + 1
						steps = []concurrencySampleStep{{parent, "running", now}, {other, "running", now}, {parent, "unknown", now}, {parent, "running", now}}
					case "heartbeat_unknown":
						now += time.Second.Milliseconds()
						steps = []concurrencySampleStep{{parent, "unknown", now}, {other, "running", now}, {parent, "unknown", now - 500}}
					}
					if scenario == "cross_group_parent" {
						child.ParentExecutionID = parent.ExecutionID
						steps = []concurrencySampleStep{{other, "running", now}, {child, "running", now}, {other, "running", now}, {child, "stopped", now}, {other, "running", now}}
					}
					if scenario == "rebound_stop" {
						steps = []concurrencySampleStep{{parent, "running", now}, {parent, "stopped", now - 1000}, {parent, "stopped", now - 2000}}
					}
					// Missing native/event fields must merge from the original fact.
					steps[len(steps)-1].source.NativeSessionID = ""
					steps[len(steps)-1].source.EventID = ""
					var samples *concurrencySamples
					if cached {
						samples = &concurrencySamples{seen: map[string]bool{}}
					}
					for index, step := range steps {
						if scenario == "rebound_stop" && index == 1 {
							if _, err := tx.Exec(t.Context(), `UPDATE concurrency_bindings SET group_id='b' WHERE environment_id='env-a'`); err != nil {
								return err
							}
							// A binding change starts a new sync, never reuses its cache.
							if cached {
								samples = &concurrencySamples{seen: map[string]bool{}}
							}
						}
						if err := putConcurrencyExecutionSampled(t.Context(), tx, step.source, step.state, step.seen, now, samples); err != nil {
							return err
						}
						var snapshot []string
						rows, err := tx.Q().QueryContext(t.Context(), `SELECT group_id,state_json FROM concurrency_groups ORDER BY group_id`)
						if err != nil {
							return err
						}
						for rows.Next() {
							var id, raw string
							if err := rows.Scan(&id, &raw); err != nil {
								rows.Close()
								return err
							}
							snapshot = append(snapshot, id+":"+raw)
						}
						err = rows.Err()
						rows.Close()
						if err != nil {
							return err
						}
						rows, err = tx.Q().QueryContext(t.Context(), `SELECT execution_id,state,source_json,last_seen,group_id,parent_execution_id FROM concurrency_executions ORDER BY execution_id`)
						if err != nil {
							return err
						}
						for rows.Next() {
							var id, state, raw, group, parent string
							var seen int64
							if err := rows.Scan(&id, &state, &raw, &seen, &group, &parent); err != nil {
								rows.Close()
								return err
							}
							encoded, err := json.Marshal([]any{id, state, raw, seen, group, parent})
							if err != nil {
								rows.Close()
								return err
							}
							snapshot = append(snapshot, string(encoded))
						}
						err = rows.Err()
						rows.Close()
						if err != nil {
							return err
						}
						snapshots = append(snapshots, snapshot)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if !cached {
					expected = snapshots
				} else if !reflect.DeepEqual(expected, snapshots) {
					t.Fatalf("sampling changed intermediate ledger states:\nreference=%v\ncached=%v", expected, snapshots)
				}
			}
		})
	}
}
