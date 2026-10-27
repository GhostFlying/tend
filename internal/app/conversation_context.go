package app

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// SetMessageAssociations records exact, versioned associations. Owner removals are tombstones.
type SetMessageAssociations struct{}

func (SetMessageAssociations) Operation() string { return "conversation.set_associations" }
func (SetMessageAssociations) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner, protocol.PrincipalMainAgent)
}
func (SetMessageAssociations) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.SetMessageAssociationsPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if len(p.WorkItemIDs) > 50 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "at most 50 associations")
	}
	old, err := store.MessageAssociations(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, errNotFound(err, "message does not exist")
	}
	if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != old.Revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "message associations changed")
	}
	desired := map[string]bool{}
	for _, id := range p.WorkItemIDs {
		if desired[id] {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "duplicate work item")
		}
		desired[id] = true
		if _, err := store.GetWorkItem(ctx, tx.Q(), id); err != nil {
			return Outcome{}, errNotFound(err, "work item does not exist")
		}
	}
	source := "agent"
	owner := x.Principal.Kind == protocol.PrincipalOwner
	if owner {
		source = "owner"
	}
	var affected []string
	for _, v := range old.Items {
		affected = append(affected, v.WorkItemID)
	}
	for id := range desired {
		if !slices.Contains(affected, id) {
			affected = append(affected, id)
		}
	}
	for _, id := range affected {
		var locked, active bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT COALESCE((SELECT owner_locked FROM message_work_items WHERE message_id=? AND work_item_id=?),0),COALESCE((SELECT active FROM message_work_items WHERE message_id=? AND work_item_id=?),0)`, old.MessageID, id, old.MessageID, id).Scan(&locked, &active); err != nil {
			return Outcome{}, err
		}
		if !owner && locked && active != desired[id] {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "owner association correction cannot be overridden")
		}
		if active == desired[id] && !owner {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO message_work_items(message_id,work_item_id,active,owner_locked,source,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(message_id,work_item_id) DO UPDATE SET active=excluded.active,owner_locked=MAX(owner_locked,excluded.owner_locked),source=excluded.source,updated_at=excluded.updated_at`, old.MessageID, id, desired[id], owner, source, x.Now.UnixMilli()); err != nil {
			return Outcome{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE background_assessments SET stale=1 WHERE conversation_id IN (SELECT conversation_id FROM agent_conversations WHERE work_item_id=?)`, id); err != nil {
			return Outcome{}, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO message_association_revisions(message_id,revision) VALUES(?,1) ON CONFLICT(message_id) DO UPDATE SET revision=revision+1`, old.MessageID); err != nil {
		return Outcome{}, err
	}
	updated, err := store.MessageAssociations(ctx, tx.Q(), old.MessageID)
	if err != nil {
		return Outcome{}, err
	}
	before, _ := json.Marshal(old.Items)
	after, _ := json.Marshal(p.WorkItemIDs)
	actor, _ := json.Marshal(x.Principal)
	if _, err := tx.Exec(ctx, `INSERT INTO message_association_history(message_id,revision,actor,before_json,after_json,created_at) VALUES(?,?,?,?,?,?)`, old.MessageID, updated.Revision, string(actor), string(before), string(after), x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	out := Outcome{Result: map[string]any{"revision": updated.Revision}, Events: []Event{{ObjectType: "message", ObjectID: old.MessageID, Kind: "conversation.associations_changed"}}}
	if owner {
		if err := queueCoordinationNotice(ctx, x, tx, "Message associations were corrected by the owner. Re-read this message and reconcile derived facts; this does not undo prior execution.", old.MessageID, "main"); err != nil {
			return Outcome{}, err
		}
		out.Notify = true
	}
	return out, nil
}

// ContextCommand persists sourced facts and baseline coverage without inventing authority.
type ContextCommand struct{ Action string }

func (c ContextCommand) Operation() string { return "work_item_context." + c.Action }
func (c ContextCommand) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalMainAgent)
}
func (c ContextCommand) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	id := x.Env.Target.ID
	var revision int64
	if err := tx.Q().QueryRowContext(ctx, `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, id).Scan(&revision); err != nil {
		return Outcome{}, errNotFound(err, "context does not exist")
	}
	if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "context changed")
	}
	if c.Action == "prepare" {
		var p protocol.PrepareWorkItemContextPayload
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		if strings.TrimSpace(p.Coverage) == "" || len(p.Coverage) > 8000 || len(p.Gaps) > 8000 {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "bounded coverage and gaps required")
		}
		var rev int64
		if err := tx.Q().QueryRowContext(ctx, `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, id).Scan(&rev); err != nil {
			return Outcome{}, errNotFound(err, "context does not exist")
		}
		if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != rev {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "context changed")
		}
		if _, err := tx.Exec(ctx, `UPDATE work_item_contexts SET baseline_revision=revision+1,revision=revision+1,coverage=?,gaps=?,prepared_by=? WHERE work_item_id=?`, p.Coverage, p.Gaps, x.Principal.Thread, id); err != nil {
			return Outcome{}, err
		}
	} else if c.Action == "fact" {
		var p protocol.ContextFact
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		if !slices.Contains([]string{"goal", "constraint", "authorization", "refusal", "commitment", "question", "suggestion"}, p.Kind) || strings.TrimSpace(p.Body) == "" || len(p.Body) > 8000 {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid fact kind or body")
		}
		msg, err := store.GetMessage(ctx, tx.Q(), p.SourceMessageID)
		if err != nil {
			return Outcome{}, errNotFound(err, "source message does not exist")
		}
		if (p.Kind == "authorization" || p.Kind == "refusal") && msg.Author != "owner" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "authority requires an original owner message")
		}
		source := protocol.ConversationItem{MessageID: msg.MessageID, Author: msg.Author, Body: msg.Body}
		if err := store.HideUnadmittedForCoordinator(ctx, tx.Q(), &source); err != nil {
			return Outcome{}, err
		}
		if source.Body != msg.Body {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "source is not admitted or has been withdrawn")
		}
		p.Attribution = msg.Author
		if msg.Author != "owner" && msg.Author != "main_agent" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "facts require original owner or assistant messages")
		}
		if p.Kind == "commitment" && msg.Author != "main_agent" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "agent commitments require an assistant source")
		}
		p.WorkItemID = ""
		if p.Scope != "project" {
			p.Project = ""
		}
		if !slices.Contains([]string{"work_item", "project", "global"}, p.Scope) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "scope must be work_item, project or global")
		}
		if p.Scope == "work_item" {
			p.WorkItemID = id
			var linked bool
			if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM message_work_items WHERE message_id=? AND work_item_id=? AND active=1)`, p.SourceMessageID, id).Scan(&linked); err != nil {
				return Outcome{}, err
			}
			if !linked {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "associate source message with work item first")
			}
		}
		if p.Scope == "project" && p.Project == "" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "project is required")
		}
		if p.Supersedes != "" {
			res, err := tx.Exec(ctx, `UPDATE work_item_context_facts SET superseded=1 WHERE fact_id=? AND scope=? AND COALESCE(work_item_id,'')=? AND project=? AND superseded=0`, p.Supersedes, p.Scope, p.WorkItemID, p.Project)
			if err != nil {
				return Outcome{}, err
			}
			n, _ := res.RowsAffected()
			if n != 1 {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "superseded fact must have the same scope")
			}
		}
		p.ID = x.IDs.New(protocol.Prefix("fact"))
		if _, err := tx.Exec(ctx, `INSERT INTO work_item_context_facts(fact_id,work_item_id,project,scope,kind,body,source_message_id,attribution,supersedes,created_at) VALUES(?,NULLIF(?,''),?,?,?,?,?,?,NULLIF(?,''),?)`, p.ID, p.WorkItemID, p.Project, p.Scope, p.Kind, p.Body, p.SourceMessageID, p.Attribution, p.Supersedes, x.Now.UnixMilli()); err != nil {
			return Outcome{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE work_item_contexts SET revision=revision+1 WHERE ?='global' OR work_item_id=? OR (?='project' AND work_item_id IN (SELECT work_item_id FROM work_items WHERE project=?))`, p.Scope, p.WorkItemID, p.Scope, p.Project); err != nil {
			return Outcome{}, err
		}
	} else {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unknown context operation")
	}
	return Outcome{Notify: true, Result: map[string]any{"work_item_id": id}, Events: []Event{{ObjectType: "work_item", ObjectID: id, Kind: "work_item.context_changed"}}}, nil
}

func queueCoordinationNotice(ctx context.Context, x *Exec, tx *store.Tx, body, source, conversation string) error {
	_, err := createCoordinationNotice(ctx, x, tx, body, source, conversation)
	return err
}

func createCoordinationNotice(ctx context.Context, x *Exec, tx *store.Tx, body, source, conversation string) (string, error) {
	id := x.IDs.New(protocol.PrefixMessage)
	if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: id, Author: "system", Body: body + "\nSource conversation: " + conversation + "\nSource message: " + source, CreatedAt: x.Now.UnixMilli()}); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO conversation_return_links(message_id,conversation_id,source_message_id) VALUES(?,?,?)`, id, conversation, source); err != nil {
		return "", err
	}
	payload, _ := json.Marshal(HandleMessage{MessageID: id})
	return id, tx.InsertOutbox(ctx, store.OutboxRow{OutboxID: x.IDs.New(protocol.PrefixOutbox), RequestID: x.IDs.New(protocol.PrefixRequest), ParentRequestID: x.Env.RequestID, Destination: MainThreadDestination, Operation: OpHandleMessage, PayloadJSON: string(payload), CreatedAt: x.Now.UnixMilli()})
}
