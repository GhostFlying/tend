package views

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Keep normal conversation reads bounded. Full originals remain available from
// the existing cursor-paged background sources endpoint and CLI command.
func backgroundPresentation(ctx context.Context, q store.Querier, batch protocol.BackgroundBatch) (protocol.BackgroundPresentation, error) {
	out := protocol.BackgroundPresentation{Sources: []protocol.BackgroundSourcePreview{}}
	var snapshot, status string
	err := q.QueryRowContext(ctx, `SELECT h.private_snapshot,t.status FROM main_turns t JOIN hosted_executions h ON h.execution_id=t.request_id WHERE t.message_id=? ORDER BY t.created_at DESC,t.request_id DESC LIMIT 1`, batch.MessageID).Scan(&snapshot, &status)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err == nil {
		var selected struct {
			Identity   protocol.AgentIdentity `json:"identity"`
			Definition struct {
				DisplayName string `json:"display_name"`
			} `json:"definition"`
		}
		if err := json.Unmarshal([]byte(snapshot), &selected); err != nil {
			return out, err
		}
		if selected.Identity.ExecutionEnvironmentID != "" && selected.Identity.LocalID != "" {
			out.Recipient = &protocol.BackgroundRecipient{Identity: selected.Identity, DisplayName: selected.Definition.DisplayName, Status: status}
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT s.seq,s.message_id,s.kind,m.body,COALESCE((SELECT b.object_type FROM conversation_bindings b WHERE b.message_id=s.message_id AND b.object_type IN ('task','issue','work_item') ORDER BY CASE b.object_type WHEN 'task' THEN 0 ELSE 1 END,b.object_id LIMIT 1),''),COALESCE((SELECT b.object_id FROM conversation_bindings b WHERE b.message_id=s.message_id AND b.object_type IN ('task','issue','work_item') ORDER BY CASE b.object_type WHEN 'task' THEN 0 ELSE 1 END,b.object_id LIMIT 1),'') FROM background_sources s JOIN conversation_messages m ON m.message_id=s.message_id WHERE s.batch_id=? AND s.cancelled_at=0 ORDER BY s.seq LIMIT 4`, batch.BatchID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var source protocol.BackgroundSourcePreview
		if err := rows.Scan(&source.Seq, &source.MessageID, &source.Kind, &source.Summary, &source.ObjectType, &source.ObjectID); err != nil {
			return out, err
		}
		if len(out.Sources) == 3 {
			out.More = true
			break
		}
		out.Sources = append(out.Sources, source)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	// Resolve only displayed bodies, after closing the page cursor. The resolver
	// retains the source's canonical content instead of exposing an archive marker.
	if err := rows.Close(); err != nil {
		return out, err
	}
	for i := range out.Sources {
		source := &out.Sources[i]
		body, err := store.ResolveMessageBody(ctx, q, source.MessageID, source.Summary)
		if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			// An optional preview must not turn a failure of one original into
			// an unreadable conversation. Its explicit source read still returns
			// the real error; this text is a display state, never replacement content.
			source.Summary = "原始来源暂时无法读取；展开后重试。"
			continue
		}
		source.Summary = strings.Join(strings.Fields(body), " ")
		if text := []rune(source.Summary); len(text) > 160 {
			source.Summary = string(text[:160]) + "…"
		}
		if source.ObjectType == "work_item" {
			canonical, err := ownerCanonicalIdentity(ctx, q, source.ObjectID)
			if err != nil {
				return out, err
			}
			if canonical {
				source.ObjectType = "issue"
			} else {
				source.ObjectType, source.ObjectID = "", ""
			}
		}
	}
	return out, nil
}
