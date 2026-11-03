package views

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
)

// ConcurrencyWaiting counts otherwise eligible actions, once per group, using
// the same host/workspace/owner facts as the queue and the claim scheduler.
func ConcurrencyWaiting(ctx context.Context, q store.Querier, now int64) (map[string]int64, error) {
	result := map[string]int64{}
	var count int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM concurrency_bindings`).Scan(&count); err != nil || count == 0 {
		return result, err
	}
	facts, err := readExecutionFacts(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT b.environment_id,b.runtime_id,b.group_id,g.state_json,(SELECT count(*) FROM concurrency_executions e JOIN concurrency_bindings eb USING(environment_id,runtime_id) WHERE eb.group_id=b.group_id AND e.state<>'stopped' AND NOT EXISTS(SELECT 1 FROM concurrency_executions child WHERE child.parent_execution_id=e.execution_id AND child.state<>'stopped')) FROM concurrency_bindings b JOIN concurrency_groups g USING(group_id) WHERE g.deleted=0`)
	if err != nil {
		return nil, err
	}
	type binding struct {
		env, runtime, group string
		g                   protocol.ConcurrencyGroup
		occupied            int64
	}
	var bindings []binding
	for rows.Next() {
		var b binding
		var raw string
		if err := rows.Scan(&b.env, &b.runtime, &b.group, &raw, &b.occupied); err != nil {
			rows.Close()
			return nil, err
		}
		var s struct {
			Group protocol.ConcurrencyGroup `json:"group"`
		}
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			rows.Close()
			return nil, err
		}
		b.g = s.Group
		bindings = append(bindings, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, host := range facts.hosts {
		if !host.available {
			continue
		}
		runtime, err := store.GetHostRuntime(ctx, q, host.ID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !runtime.Online || now-runtime.LastHeartbeatAt > 60000 || !slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityAdaptiveConcurrency) {
			continue
		}
		actual := host.Host
		actual.ConcurrencyBlocked = map[string]bool{}
		for _, b := range bindings {
			if b.env == host.ID {
				actual.ConcurrencyBlocked[b.runtime] = b.occupied >= b.g.Current || now < b.g.CooldownUntil
			}
		}
		for _, c := range facts.candidates {
			if scheduler.Skip(host.Host, c) != "" || scheduler.Skip(actual, c) == "" {
				continue
			}
			for _, b := range bindings {
				if b.env != host.ID || !actual.ConcurrencyBlocked[b.runtime] {
					continue
				}
				specific := host.Host
				specific.ConcurrencyBlocked = map[string]bool{}
				for _, e := range append(slices.Clone(host.Executors), host.Continuations...) {
					specific.ConcurrencyBlocked[e.Pin.Runtime.RuntimeID] = e.Pin.Runtime.RuntimeID != b.runtime
				}
				if scheduler.Skip(specific, c) != "" {
					continue
				}
				pin, reason := scheduler.SelectExecution(specific, c)
				if reason != "" || pin == nil || pin.Runtime.RuntimeID != b.runtime {
					continue
				}
				key := b.group + "/" + c.ActionID
				if !seen[key] {
					seen[key] = true
					result[b.group]++
				}
			}
		}
	}
	return result, nil
}
