package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// priorTakeoverObservation reads the real observation or legacy transition. A
// Run may have several new observations after hand-back; only one identity is
// a retry. No RPC response or request-to-object mapping is retained.
func priorTakeoverObservation(ctx context.Context, q store.Querier, run store.RunRow, p protocol.TakeoverPayload) (bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT kind,body_json,COALESCE(request_id,''),COALESCE(actor_json,'') FROM events WHERE (object_type='run' AND object_id=? AND kind='run.takeover_observed') OR (object_type='task' AND object_id=? AND kind='task.taken_over') ORDER BY event_seq`, run.RunID, run.TaskID)
	if err != nil {
		return false, err
	}
	type fact struct{ kind, body, request, actor string }
	facts := []fact{}
	for rows.Next() {
		var f fact
		if err := rows.Scan(&f.kind, &f.body, &f.request, &f.actor); err != nil {
			rows.Close()
			return false, err
		}
		facts = append(facts, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, f := range facts {
		raw, err := store.ResolveJSON(ctx, q, f.body)
		if err != nil {
			return false, err
		}
		var body struct {
			RunID         string `json:"run_id"`
			ObservationID string `json:"observation_id"`
			Reason        string `json:"reason"`
		}
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			return false, fmt.Errorf("read takeover observation: %w", err)
		}
		if body.RunID != run.RunID {
			continue
		}
		if body.ObservationID == "" && f.kind == "task.taken_over" {
			body.ObservationID = protocol.TakeoverObservationFromHistoricalEvent(run.RunID, f.request)
			if body.ObservationID == p.ObservationID {
				var actor protocol.Principal
				if json.Unmarshal([]byte(f.actor), &actor) != nil || actor.Kind != protocol.PrincipalRun || actor.Subject != run.RunID {
					return false, protocol.Errorf(protocol.CodeUnknownOutcome, "legacy takeover provenance is unavailable; reconcile the original observation")
				}
			}
		}
		if body.ObservationID != p.ObservationID {
			continue
		}
		if body.Reason != p.Reason {
			return false, protocol.Errorf(protocol.CodeConflictPayload, "takeover observation already has another reason")
		}
		return true, nil
	}
	return false, nil
}
