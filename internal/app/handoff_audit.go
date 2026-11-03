package app

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// HandoffAudit reads only recorded events. The instance filter can select legacy
// records through their retained object relationship without inventing identity.
func (a *App) HandoffAudit(ctx context.Context, instance string, after int64, limit int) (protocol.HandoffAuditPage, error) {
	out := protocol.HandoffAuditPage{Instance: instance, Items: []protocol.HandoffAuditEvent{}, NextAfter: after}
	if strings.TrimSpace(instance) == "" || len(instance) > 512 || after < 0 || limit < 1 || limit > 200 {
		return out, protocol.Errorf(protocol.CodeInvalidRequest, "instance, nonnegative after and limit between 1 and 200 required")
	}
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		rows, err := tx.Q().QueryContext(ctx, `SELECT event_seq,event_id,object_id,kind,body_json,created_at FROM events e WHERE event_seq>? AND source='mainthread' AND object_type='conversation' AND kind IN ('conversation.summary','conversation.summary_started','conversation.summary_failed','conversation.summary_succeeded','conversation.handoff_prepared','conversation.handoff_advanced','conversation.handoff_refreshed','conversation.handoff_retargeted','conversation.handoff_target','conversation.handoff_completed') AND (CASE WHEN json_extract(body_json,'$.format')=1 THEN json_extract(body_json,'$.instance')=? ELSE (object_id=? AND kind IN ('conversation.summary_started','conversation.summary_failed','conversation.summary_succeeded')) OR EXISTS(SELECT 1 FROM main_thread_handoffs h WHERE h.handoff_id=e.object_id AND h.instance=?) OR EXISTS(SELECT 1 FROM conversation_summaries s WHERE s.summary_id=e.object_id AND s.instance=?) END) ORDER BY event_seq LIMIT ?`, after, instance, instance, instance, instance, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item protocol.HandoffAuditEvent
			var raw string
			if err := rows.Scan(&item.Sequence, &item.EventID, &item.ObjectID, &item.Kind, &raw, &item.CreatedAtMillis); err != nil {
				return err
			}
			if len(out.Items) == limit {
				out.More = true
				break
			}
			item.IdentityObservation = "observation_unavailable"
			var identity protocol.HandoffAuditIdentity
			var fields map[string]json.RawMessage
			validFields := json.Unmarshal([]byte(raw), &fields) == nil && len(fields["source_version"]) != 0 && string(fields["source_version"]) != "null" && len(fields["source_generation"]) != 0 && string(fields["source_generation"]) != "null"
			if validFields && json.Unmarshal([]byte(raw), &identity) == nil && identity.Format == 1 && identity.Instance == instance && identity.SourceThreadID != "" && identity.SourceVersion >= 0 && identity.SourceGeneration >= 0 {
				item.Identity, item.IdentityObservation = &identity, "recorded"
			}
			out.Items = append(out.Items, item)
			out.NextAfter = item.Sequence
		}
		return rows.Err()
	})
	return out, err
}

func summaryAuditIdentity(snap store.HandoffSnapshot) protocol.HandoffAuditIdentity {
	return protocol.HandoffAuditIdentity{Format: 1, Instance: snap.Instance, SourceThreadID: snap.ThreadID, SourceVersion: snap.Version, SourceGeneration: snap.BindingGeneration}
}

func handoffAuditIdentity(h store.ThreadHandoff) protocol.HandoffAuditIdentity {
	return protocol.HandoffAuditIdentity{Format: 1, Instance: h.Instance, SourceThreadID: h.SourceThreadID, SourceVersion: h.SourceVersion, SourceGeneration: h.SourceGeneration, TargetThreadID: h.TargetThreadID, HandoffID: h.ID, Phase: h.Phase}
}
