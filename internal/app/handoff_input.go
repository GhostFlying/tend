package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// SummaryInput freezes current ledger objects (L) and historical corrections (D).
// Native history (H) is supplied exclusively by the isolated harness fork.
type summaryCorrection struct {
	protocol.ConversationItem
	CoverageUncertain   bool   `json:"coverage_uncertain"`
	Withdrawn           bool   `json:"withdrawn"`
	NativeBodyUnchanged bool   `json:"native_body_unchanged,omitempty"`
	BodyReference       string `json:"body_reference,omitempty"`
}

type SummaryInput struct {
	Snapshot        store.HandoffSnapshot
	PreviousSummary *store.HandoffSummary
	Observations    json.RawMessage
	Ledger          []json.RawMessage
}

// HandoffSummaryInput uses pageBytes as a read-page hint, never a total budget.
// All pages belong to one database snapshot; historical tool I/O is excluded.
func (a *App) HandoffSummaryInput(ctx context.Context, instance string, pageBytes int) (out SummaryInput, err error) {
	if pageBytes <= 0 {
		return out, protocol.Errorf(protocol.CodeInvalidRequest, "positive summary page size required")
	}
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		q := tx.Q()
		var err error
		out.Snapshot, err = store.GetHandoffSnapshot(ctx, q, instance)
		if err != nil {
			return err
		}
		if out.Snapshot.Active || out.Snapshot.Pending || out.Snapshot.ThreadID == "" {
			return protocol.Errorf(protocol.CodeInvalidTransition, "summary requires an idle bound conversation")
		}
		previous, err := store.GetHandoffSummary(ctx, q, instance)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		since := int64(-1)
		if err == nil && previous.FormatVersion >= 3 && previous.Snapshot.Summary != nil && previous.Snapshot.Summary.Complete && previous.Snapshot.Version <= out.Snapshot.Version {
			out.PreviousSummary = &previous
			since = previous.Snapshot.ConversationVersion
		}
		// Each record carries its object type; all current effective objects are read.
		sections := []struct{ kind, query string }{
			{"work_item", `SELECT body_json,revision,status FROM work_items WHERE status<>'closed' ORDER BY work_item_id`},
			{"context", `SELECT work_item_id,revision,baseline_revision,coverage,gaps FROM work_item_contexts ORDER BY work_item_id`},
			{"context_obligation", `SELECT message_id,work_item_id,needs_review FROM message_context_obligations WHERE needs_review=1 ORDER BY work_item_id,message_id`},
			{"logical_turn", `SELECT logical_turn_id,message_id,status,repair_attempts,reason,revision,resumed_by FROM logical_turns WHERE status<>'completed' ORDER BY logical_turn_id`},
			{"message_review", `SELECT d.message_id,d.content_revision,d.classified_revision,d.facts_revision,d.disposition FROM message_dispositions d JOIN conversation_messages m ON m.message_id=d.message_id WHERE m.conversation_id='main' AND m.kind='message' AND (d.disposition='pending' OR d.classified_revision<>d.content_revision OR d.facts_revision<>d.content_revision) ORDER BY d.message_id`},
			{"fact", `SELECT f.fact_id,f.work_item_id,f.project,f.scope,f.kind,f.body,f.source_message_id,f.attribution,f.needs_review,d.content_revision AS source_content_revision,COALESCE((SELECT revision FROM message_association_revisions r WHERE r.message_id=f.source_message_id),0) AS source_association_revision FROM work_item_context_facts f LEFT JOIN message_dispositions d ON d.message_id=f.source_message_id WHERE f.superseded=0 ORDER BY f.fact_id`},
			{"task", `SELECT t.task_id,t.title,t.project,t.lifecycle,t.control_mode,t.control_revision,t.spec_version,t.block_reason,t.current_artifact_id,json_extract(s.body_json,'$.goal') AS goal,json_extract(s.body_json,'$.scope') AS scope,json_extract(s.body_json,'$.inputs') AS inputs_json,json_extract(s.body_json,'$.shared_context') AS shared_context_json,json_extract(s.body_json,'$.constraints') AS constraints_json,json_extract(s.body_json,'$.acceptance') AS acceptance_json,json_extract(s.body_json,'$.deliverables') AS deliverables_json,json_extract(s.body_json,'$.acceptance_execution') AS acceptance_execution_json FROM tasks t JOIN task_specs s ON s.task_id=t.task_id AND s.spec_version=t.spec_version WHERE t.lifecycle NOT IN ('completed','cancelled') ORDER BY t.task_id`},
			{"run", `SELECT run_id,task_id,role,host_id,desired_state,observed_state,generation FROM runs WHERE observed_state IN ('starting','running','waiting','interrupted','unknown') ORDER BY run_id`},
			{"decision", `SELECT decision_id,task_id,version,status,question,options_json FROM decisions WHERE status='open' ORDER BY decision_id`},
			{"decision_briefing", `SELECT b.decision_id,b.revision,b.body_json FROM decision_briefings b JOIN decisions d ON d.decision_id=b.decision_id WHERE d.status='open' AND b.revision=(SELECT MAX(current.revision) FROM decision_briefings current WHERE current.decision_id=b.decision_id) ORDER BY b.decision_id`},
			{"artifact", `SELECT a.artifact_id,a.task_id,a.run_id,a.kind,a.digest,json_extract(a.ref_json,'$.report.title') AS report_title,json_extract(a.ref_json,'$.files') AS files_json,json_extract(a.ref_json,'$.mounts') AS mounts_json FROM artifacts a WHERE a.artifact_id IN (SELECT current_artifact_id FROM tasks WHERE current_artifact_id IS NOT NULL) ORDER BY a.task_id`},
			{"acceptance", `SELECT acceptance_id,task_id,artifact_id,spec_version,verdict FROM acceptances WHERE artifact_id IN (SELECT current_artifact_id FROM tasks WHERE current_artifact_id IS NOT NULL) ORDER BY acceptance_id`},
			{"choice", `SELECT choice_id,work_item_id,status,version,body_json,message_id FROM work_item_choices WHERE status='open' ORDER BY choice_id`},
		}
		sections = append(sections, struct{ kind, query string }{"delivery", `SELECT o.work_item_id,o.delivery_id,o.observation_json,o.refresh_requested FROM delivery_observations o JOIN work_items w ON w.work_item_id=o.work_item_id WHERE w.status<>'closed' ORDER BY o.work_item_id,o.delivery_id`}, struct{ kind, query string }{"followup", `SELECT rule_id,work_item_id,revision,status,next_due,body_json FROM followup_rules WHERE status IN ('active','paused') ORDER BY rule_id`})
		sections = append(sections, struct{ kind, query string }{"closed_work_item", `SELECT work_item_id,revision,status,json_extract(body_json,'$.title') AS title,json_extract(body_json,'$.closure') AS closure_json FROM work_items WHERE status='closed' ORDER BY work_item_id`}, struct{ kind, query string }{"terminal_task", `SELECT task_id,title,lifecycle,spec_version,current_artifact_id FROM tasks WHERE lifecycle IN ('completed','cancelled') ORDER BY task_id`})
		for _, section := range sections {
			records, err := summaryObjects(ctx, q, section.kind, section.query)
			if err != nil {
				return err
			}
			out.Ledger = append(out.Ledger, records...)
		}
		observations := []any{}
		// Page by durable message ID under the same read transaction, including tombstones.
		after := ""
		pageRows := max(1, min(pageBytes/512, 512))
		for {
			rows, err := q.QueryContext(ctx, `SELECT c.message_id FROM conversation_changes c LEFT JOIN conversation_messages m ON m.message_id=c.message_id LEFT JOIN message_dispositions d ON d.message_id=m.message_id WHERE (c.last_version>? OR m.message_id IS NULL OR d.content_revision>1 OR d.owner_locked=1 OR EXISTS(SELECT 1 FROM message_association_history a WHERE a.message_id=c.message_id) OR EXISTS(SELECT 1 FROM conversation_message_controls x WHERE x.message_id=m.message_id AND x.withdrawn_at<>0)) AND c.message_id>? AND (m.message_id IS NULL OR (m.conversation_id='main' AND m.kind='message')) ORDER BY c.message_id LIMIT ?`, since, after, pageRows)
			if err != nil {
				return err
			}
			ids := []string{}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				ids = append(ids, id)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if len(ids) == 0 {
				break
			}
			for _, id := range ids {
				row, err := store.GetConversationItem(ctx, q, id)
				if errors.Is(err, store.ErrNotFound) {
					observations = append(observations, map[string]any{"message_id": id, "deleted": true})
					continue
				}
				if err != nil {
					return err
				}
				// Known unmodified text visible in the fork need not be copied.
				// A prior summary can still contain an obsolete delivery state;
				// later version changes must supply current metadata in D.
				assoc, err := store.MessageAssociations(ctx, q, id)
				if err != nil {
					return err
				}
				var native bool
				if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM main_turns WHERE message_id=? AND thread_id=? AND status='completed') OR EXISTS(SELECT 1 FROM conversation_agent_messages WHERE message_id=? AND thread_id=? AND status='completed')`, id, out.Snapshot.ThreadID, id, out.Snapshot.ThreadID).Scan(&native); err != nil {
					return err
				}
				var withdrawn bool
				if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_message_controls WHERE message_id=? AND withdrawn_at<>0) OR EXISTS(SELECT 1 FROM followup_occurrences WHERE message_id=? AND status='cancelled')`, id, id).Scan(&withdrawn); err != nil {
					return err
				}
				var associationChanged bool
				if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM message_association_history WHERE message_id=?)`, id).Scan(&associationChanged); err != nil {
					return err
				}
				if native && assoc.ContentRevision <= 1 && !assoc.OwnerLocked && !associationChanged && !withdrawn && out.PreviousSummary == nil {
					continue
				}
				it := protocol.ConversationItem{Seq: row.Seq, MessageID: id, Kind: row.Kind, Author: row.Author, Body: row.Body, ReplyTo: row.ReplyTo, CreatedAt: time.UnixMilli(row.CreatedAt).UTC(), State: protocol.MessageState(row.State), AssistantState: protocol.AssistantState(row.AssistantState), AssistantRevision: row.AssistantRevision, Associations: &assoc}
				it.Images, err = store.ConversationImages(ctx, q, id)
				if err != nil {
					return err
				}
				it.Attachments, err = store.ConversationAttachments(ctx, q, id)
				if err != nil {
					return err
				}
				if row.DecisionJSON != "" {
					var choice protocol.DecisionChoice
					if err := json.Unmarshal([]byte(row.DecisionJSON), &choice); err != nil {
						return err
					}
					it.Decision = &choice
				}
				if err := store.HideUndeliveredOwner(ctx, q, &it); err != nil {
					return err
				}
				if row.Author == "owner" && it.Body != row.Body && !withdrawn {
					continue
				}
				// Corrections are explicitly authoritative over old history and summaries.
				priorCoversBody := false
				if out.PreviousSummary != nil {
					var version int64
					if err := q.QueryRowContext(ctx, `SELECT last_version FROM conversation_changes WHERE message_id=?`, id).Scan(&version); err != nil {
						return err
					}
					priorCoversBody = version <= out.PreviousSummary.Snapshot.ConversationVersion
				}
				unchanged := (native && assoc.ContentRevision <= 1 || priorCoversBody) && !withdrawn
				reference := ""
				if unchanged && priorCoversBody {
					reference = "previous_summary"
				}
				if unchanged {
					// History already has the text. Retain exact corrected labels,
					// not a second replay of every classified chat message.
					it.Body = ""
					it.Images, it.Attachments = nil, nil
				}
				observations = append(observations, summaryCorrection{ConversationItem: it, CoverageUncertain: !native || associationChanged, Withdrawn: withdrawn, NativeBodyUnchanged: unchanged && native && assoc.ContentRevision <= 1, BodyReference: reference})
			}
			after = ids[len(ids)-1]
		}
		out.Observations, err = json.Marshal(observations)
		if err != nil {
			return err
		}
		ledgerJSON, err := json.Marshal(out.Ledger)
		if err != nil {
			return err
		}
		out.Snapshot.Summary = &store.SummaryCoverage{LedgerRecords: len(out.Ledger), LedgerBytes: len(ledgerJSON), CorrectionRecords: len(observations), CorrectionBytes: len(out.Observations), Complete: true}
		return nil
	})
	if err != nil {
		return SummaryInput{}, err
	}
	return out, nil
}

func summaryObjects(ctx context.Context, q store.Querier, kind, query string) ([]json.RawMessage, error) {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("summary %s: %w", kind, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := []json.RawMessage{}
	for rows.Next() {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		record := map[string]any{"object_type": kind}
		for i, name := range columns {
			value := values[i]
			if text, ok := value.(string); ok && len(name) >= 5 && name[len(name)-5:] == "_json" {
				var v any
				if json.Unmarshal([]byte(text), &v) == nil {
					value = v
				}
			}
			record[name] = value
		}
		b, err := json.Marshal(record)
		if err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, rows.Err()
}
