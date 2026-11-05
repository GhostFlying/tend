package views

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// capabilitySelections reuses the spec already decoded by the single queue
// scan. Tasks without a current candidate are read in bounded batches, keeping
// relation and snapshot reads independent of per-task capability lookups. Each
// supplied row keeps its spec version even if the current task has advanced.
func (facts executionFacts) capabilitySelections(ctx context.Context, q store.Querier, rows []store.TaskRow) (map[string]*protocol.TaskAgentSelection, error) {
	out := map[string]*protocol.TaskAgentSelection{}
	var pending []store.TaskRow
	put := func(id string, s *protocol.TaskAgentSelection) {
		if s != nil && (len(s.Implementation.RequiredCapabilities) > 0 || len(s.Acceptance.RequiredCapabilities) > 0) {
			copy := s.Clone()
			out[id] = &copy
		}
	}
	for _, row := range rows {
		if row.Kind == protocol.TaskKindIssueManager {
			continue
		}
		if c, ok := facts.candidates[row.TaskID]; ok && c.TargetSpecVersion == row.SpecVersion {
			put(row.TaskID, c.AgentSelection)
		} else {
			pending = append(pending, row)
		}
	}
	for start := 0; start < len(pending); start += 200 {
		ids := pending[start:min(start+200, len(pending))]
		args := make([]any, 0, 2*len(ids))
		for _, row := range ids {
			args = append(args, row.TaskID, row.SpecVersion)
		}
		query := `WITH requested(task_id,spec_version) AS (VALUES ` + strings.TrimSuffix(strings.Repeat("(?,?),", len(ids)), ",") + `) SELECT s.task_id,s.body_json FROM task_specs s JOIN requested r ON r.task_id=s.task_id AND r.spec_version=s.spec_version WHERE (json_array_length(s.body_json,'$.agent_selection.implementation.required_capabilities')>0 OR json_array_length(s.body_json,'$.agent_selection.acceptance.required_capabilities')>0)`
		found, err := q.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for found.Next() {
			var id, body string
			if err := found.Scan(&id, &body); err != nil {
				found.Close()
				return nil, err
			}
			var s struct {
				Selection *protocol.TaskAgentSelection `json:"agent_selection"`
			}
			if err := json.Unmarshal([]byte(body), &s); err != nil {
				found.Close()
				return nil, err
			}
			put(id, s.Selection)
		}
		err = found.Err()
		found.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
