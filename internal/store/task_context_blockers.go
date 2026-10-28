package store

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
)

// TaskContextBlockers exposes stale execution bindings separately from semantic
// context readiness, so it cannot prevent a background reader from reconciling.
func TaskContextBlockers(ctx context.Context, q Querier, item string) ([]protocol.ContextBlocker, error) {
	rows, err := q.QueryContext(ctx, `SELECT t.task_id,t.spec_version,t.control_revision,s.body_json FROM tasks t JOIN work_item_task_links l ON l.task_id=t.task_id JOIN task_specs s ON s.task_id=t.task_id AND s.spec_version=t.spec_version WHERE l.work_item_id=? AND t.lifecycle NOT IN ('completed','cancelled') ORDER BY t.task_id`, item)
	if err != nil {
		return nil, err
	}
	type binding struct {
		id      string
		version int64
		control int64
		digest  string
	}
	var bindings []binding
	for rows.Next() {
		var id, raw string
		var version, control int64
		if err := rows.Scan(&id, &version, &control, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		var spec struct {
			Contexts []protocol.TaskWorkContext `json:"shared_context"`
		}
		if err := json.Unmarshal([]byte(raw), &spec); err != nil {
			rows.Close()
			return nil, err
		}
		for _, c := range spec.Contexts {
			if c.WorkItemID == item && c.ContextDigest != "" {
				bindings = append(bindings, binding{id, version, control, c.ContextDigest})
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []protocol.ContextBlocker{}
	if len(bindings) == 0 {
		return out, nil
	}
	digest, _, err := EffectiveContext(ctx, q, item)
	if err != nil {
		return nil, err
	}
	for _, b := range bindings {
		if b.digest != digest {
			out = append(out, protocol.ContextBlocker{Code: "task_agreement", ObjectID: b.id, Revision: b.control, ControlRevision: b.control, SpecVersion: b.version, Message: "task's frozen source context differs; explicitly revise its agreement or keep it paused"})
		}
	}
	return out, nil
}
