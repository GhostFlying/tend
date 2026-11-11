package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

const FeishuDestination = "messaging:feishu"

type FeishuCommand struct{ Unbind bool }

func (c FeishuCommand) Operation() string {
	if c.Unbind {
		return "messaging.feishu_unbind"
	}
	return "messaging.feishu_bind"
}
func (FeishuCommand) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (c FeishuCommand) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "channel" || x.Env.Target.ID != "feishu" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "Feishu binding target is required")
	}
	rev, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	var input protocol.BindFeishu
	if !c.Unbind {
		if err = decodePayload(x.Env, &input); err != nil {
			return Outcome{}, err
		}
		for _, v := range []string{input.AppID, input.TenantKey, input.SenderID, input.ChatID} {
			if strings.TrimSpace(v) == "" || len(v) > 256 {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "bounded app, tenant, sender and chat IDs are required")
			}
		}
	}
	result, err := tx.Exec(ctx, `UPDATE feishu_binding SET owner=?,app_id=?,tenant_key=?,sender_id=?,chat_id=?,enabled=?,revision=revision+1 WHERE singleton=1 AND revision=?`, x.Principal.Subject, input.AppID, input.TenantKey, input.SenderID, input.ChatID, !c.Unbind, rev)
	if err != nil {
		return Outcome{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return Outcome{}, err
	}
	if n != 1 {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "Feishu binding changed")
	}
	status := "bound"
	if c.Unbind {
		status = "disabled"
	}
	if _, err = tx.Exec(ctx, `UPDATE messaging_channels SET owner=?,status=?,revision=?,updated_at=? WHERE channel_id='feishu'`, x.Principal.Subject, status, rev+1, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: protocol.FeishuBindingView{Owner: x.Principal.Subject, BindFeishu: input, Revision: rev + 1, Enabled: !c.Unbind}, Events: []Event{{ObjectType: "channel", ObjectID: "feishu", Kind: "messaging.channel_changed"}}}, nil
}

type feishuBinding struct{ protocol.FeishuBindingView }

func readFeishuBinding(ctx context.Context, q store.Querier) (feishuBinding, error) {
	var b feishuBinding
	err := q.QueryRowContext(ctx, `SELECT b.owner,b.app_id,b.tenant_key,b.sender_id,b.chat_id,b.revision,b.enabled,c.health FROM feishu_binding b JOIN messaging_channels c ON c.channel_id='feishu' WHERE singleton=1`).Scan(&b.Owner, &b.AppID, &b.TenantKey, &b.SenderID, &b.ChatID, &b.Revision, &b.Enabled, &b.RuntimeHealth)
	return b, err
}
func (a *App) FeishuBinding(ctx context.Context) (protocol.FeishuBindingView, error) {
	var b feishuBinding
	err := a.store.Read(ctx, func(tx *store.ReadTx) error { var err error; b, err = readFeishuBinding(ctx, tx.Q()); return err })
	return b.FeishuBindingView, err
}

// FeishuIncoming is built by the authenticated transport. It grants no authority
// until its complete app/tenant/sender/chat tuple matches the owner binding.
type FeishuIncoming struct{ EventID, MessageID, AppID, TenantKey, SenderID, ChatID, ParentID, Text string }
type feishuInput struct {
	Input    FeishuIncoming
	Revision int64
}

func (feishuInput) Operation() string { return "messaging.feishu_input" }
func (c feishuInput) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	if err := allowKinds(x.Principal, protocol.PrincipalOwner); err != nil {
		return err
	}
	b, err := readFeishuBinding(ctx, q)
	if err != nil {
		return err
	}
	m := c.Input
	if !b.Enabled || b.Owner != x.Principal.Subject || b.Revision != c.Revision || b.AppID != m.AppID || b.TenantKey != m.TenantKey || b.SenderID != m.SenderID || b.ChatID != m.ChatID {
		return protocol.Errorf(protocol.CodeForbidden, "Feishu source is not bound")
	}
	return nil
}
func (c feishuInput) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	m := c.Input
	var existing, state string
	if err := tx.Q().QueryRowContext(ctx, `SELECT message_id,state FROM feishu_inputs WHERE event_id=? AND owner=? AND binding_revision=?`, m.EventID, x.Principal.Subject, c.Revision).Scan(&existing, &state); err != nil {
		return Outcome{}, err
	}
	if existing != "" {
		return Outcome{NoEffect: true, Result: map[string]any{"message_id": existing}}, nil
	}
	if state == "failed" || state == "unknown" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "original provider input requires reconciliation")
	}

	p := protocol.MessagingInput{MessageID: protocol.NativeObjectID(protocol.PrefixMessage, "feishu:"+m.AppID, m.EventID), Body: m.Text}
	if m.ParentID != "" {
		if err := tx.Q().QueryRowContext(ctx, `SELECT message_id FROM feishu_inputs WHERE external_message_id=? AND message_id<>'' AND owner=? AND binding_revision=? UNION ALL SELECT d.message_id FROM feishu_deliveries f JOIN messaging_deliveries d USING(delivery_id) WHERE f.external_message_id=? AND d.channel_id='feishu' AND d.binding_revision=? LIMIT 1`, m.ParentID, x.Principal.Subject, c.Revision, m.ParentID, c.Revision).Scan(&p.ReplyTo); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Outcome{}, protocol.Errorf(protocol.CodeNotFound, "quoted Feishu message is not known")
			}
			return Outcome{}, err
		}
	}
	out, err := submitCommunication(ctx, x, tx, p, "feishu", 0, 0, 0, false)
	if err != nil {
		return Outcome{}, err
	}
	id := out.Result.(map[string]any)["message_id"].(string)
	err = bindFeishuInputSource(ctx, x, tx, id)
	return out, err
}

// bindFeishuInputSource uses the authenticated command's durable request identity.
// It runs before any synchronous acknowledgement publishes a reply to this input.
func bindFeishuInputSource(ctx context.Context, x *Exec, tx *store.Tx, id string) error {
	result, err := tx.Exec(ctx, `UPDATE feishu_inputs SET message_id=?,state='succeeded' WHERE request_id=? AND owner=? AND (message_id='' OR message_id=?)`, id, x.Env.RequestID, x.Principal.Subject, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return protocol.Errorf(protocol.CodeConflictPayload, "Feishu input source changed")
	}
	return nil
}

// ReceiveFeishu persists the same request identity before invoking the business
// command. A callback retry never changes its payload or binding generation.
func (a *App) ReceiveFeishu(ctx context.Context, m FeishuIncoming) (protocol.Operation, error) {
	if m.EventID == "" || len(m.EventID) > 256 || m.MessageID == "" || len(m.MessageID) > 256 {
		return protocol.Operation{}, protocol.Errorf(protocol.CodeInvalidRequest, "provider event identity is required")
	}
	done, err := a.BeginOwnerMutation(ctx)
	if err != nil {
		return protocol.Operation{}, err
	}
	defer done()
	raw, _ := json.Marshal(m)
	request, owner := "", ""
	var revision int64
	err = a.write(ctx, func(tx *store.Tx) error {
		b, err := readFeishuBinding(ctx, tx.Q())
		if err != nil {
			return err
		}
		if !b.Enabled || b.AppID != m.AppID || b.TenantKey != m.TenantKey || b.SenderID != m.SenderID || b.ChatID != m.ChatID {
			return protocol.Errorf(protocol.CodeForbidden, "Feishu source is not bound")
		}
		var prior string
		err = tx.Q().QueryRowContext(ctx, `SELECT payload,request_id,owner,binding_revision FROM feishu_inputs WHERE event_id=? OR external_message_id=?`, m.EventID, m.MessageID).Scan(&prior, &request, &owner, &revision)
		if err == nil {
			if prior != string(raw) {
				return protocol.Errorf(protocol.CodeConflictPayload, "provider event content changed")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		request = a.ids.New(protocol.PrefixRequest)
		owner = b.Owner
		revision = b.Revision
		_, err = tx.Exec(ctx, `INSERT INTO feishu_inputs(event_id,external_message_id,request_id,payload,owner,binding_revision) VALUES(?,?,?,?,?,?)`, m.EventID, m.MessageID, request, string(raw), owner, revision)
		return err
	})
	if err != nil {
		return protocol.Operation{}, err
	}
	if m.ParentID != "" {
		var pending bool
		if err := a.store.Read(ctx, func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM feishu_inputs parent WHERE parent.external_message_id=? AND parent.owner=? AND parent.binding_revision=? AND parent.message_id='' AND parent.state IN ('pending','accepted','unknown'))`, m.ParentID, owner, revision).Scan(&pending)
		}); err != nil {
			return protocol.Operation{}, err
		}
		if pending {
			return protocol.Operation{}, protocol.Errorf(protocol.CodeUnknownOutcome, "quoted Feishu input is pending")
		}
	}
	payload, _ := json.Marshal(m)
	env := &protocol.Envelope{RequestID: request, Operation: "messaging.feishu_input", Target: protocol.Target{Type: "message"}, Payload: payload}
	op, err := a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalOwner, Subject: owner, Client: "feishu"}, env, feishuInput{Input: m, Revision: revision})
	if err == nil && op.Status == protocol.OpFailed {
		code := ""
		if op.Error != nil {
			code = string(op.Error.Code)
		}
		if persistErr := a.write(ctx, func(tx *store.Tx) error {
			_, e := tx.Exec(ctx, `UPDATE feishu_inputs SET state='failed',failure_code=? WHERE event_id=? AND message_id='' AND state='pending'`, code, m.EventID)
			return e
		}); persistErr != nil {
			return protocol.Operation{}, persistErr
		}
	}
	return op, err
}

// FeishuPart freezes provider identity and the accepted binding generation.
type FeishuPart struct {
	DeliveryID string `json:"delivery_id"`
	MessageID  string `json:"message_id"`
	AppID      string `json:"app_id"`
	TenantKey  string `json:"tenant_key"`
	ChatID     string `json:"chat_id"`
	Revision   int64  `json:"binding_revision"`
	Body       string `json:"body"`
}

func queueFeishuCommunication(ctx context.Context, x *Exec, tx *store.Tx, id string, p protocol.MessagingPublication) error {
	b, err := readFeishuBinding(ctx, tx.Q())
	if err != nil {
		return err
	}
	if !b.Enabled && p.ReplyTo == "" {
		return nil
	}
	original := b
	if p.ReplyTo != "" {
		var raw string
		if err := tx.Q().QueryRowContext(ctx, `SELECT payload,owner,binding_revision FROM feishu_inputs WHERE message_id=?`, p.ReplyTo).Scan(&raw, &original.Owner, &original.Revision); err != nil {
			return errNotFound(err, "Feishu reply source is unavailable")
		}
		var source FeishuIncoming
		if err := json.Unmarshal([]byte(raw), &source); err != nil {
			return err
		}
		original.AppID, original.TenantKey, original.ChatID, original.SenderID = source.AppID, source.TenantKey, source.ChatID, source.SenderID
	}
	sourceCurrent := b.Owner == original.Owner && b.Revision == original.Revision && b.AppID == original.AppID && b.TenantKey == original.TenantKey && b.ChatID == original.ChatID && b.SenderID == original.SenderID
	body := p.Body
	interaction, err := interactionForMessage(ctx, tx.Q(), id)
	if err != nil {
		return err
	}
	if interaction != nil {
		body = MessagingInteractionText(*interaction)
		if p.Body != interaction.Question {
			body = p.Body + "\n\n" + body
		}
	}
	parts := splitCommunication(body, 1000)
	for n, body := range parts {
		state := "queued"
		if !b.Enabled || b.RuntimeHealth == "disabled" || !sourceCurrent {
			state = "unavailable"
		}
		part := FeishuPart{DeliveryID: x.IDs.New(protocol.PrefixOutbox), MessageID: id, AppID: original.AppID, TenantKey: original.TenantKey, ChatID: original.ChatID, Revision: original.Revision, Body: body}
		if _, err = tx.Exec(ctx, `INSERT INTO messaging_deliveries(delivery_id,message_id,channel_id,part,kind,body,state,created_at,updated_at,binding_revision) VALUES(?,?,'feishu',?,'text',?,?,?,?,?)`, part.DeliveryID, id, n, body, state, x.Now.UnixMilli(), x.Now.UnixMilli(), original.Revision); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO feishu_deliveries(delivery_id) VALUES(?)`, part.DeliveryID); err != nil {
			return err
		}
		if state == "queued" {
			raw, _ := json.Marshal(part)
			if err = tx.InsertOutbox(ctx, store.OutboxRow{OutboxID: part.DeliveryID, RequestID: x.IDs.New(protocol.PrefixRequest), ParentRequestID: x.Env.RequestID, Destination: FeishuDestination, Operation: "messaging.feishu_send", PayloadJSON: string(raw), CreatedAt: x.Now.UnixMilli()}); err != nil {
				return err
			}
		}
	}
	for n, file := range p.Attachments {
		if _, err = tx.Exec(ctx, `INSERT INTO messaging_deliveries(delivery_id,message_id,channel_id,part,kind,file_id,state,reason,created_at,updated_at,binding_revision) VALUES(?,?,'feishu',?,'file',?,'unavailable','feishu_attachments_unsupported',?,?,?)`, x.IDs.New(protocol.PrefixOutbox), id, len(parts)+n, file, x.Now.UnixMilli(), x.Now.UnixMilli(), original.Revision); err != nil {
			return err
		}
	}
	return nil
}

// ConfigureFeishuRuntime records availability of a configured transport, not a
// provider authentication or delivery success. Credentials are never stored.
func (a *App) ConfigureFeishuRuntime(ctx context.Context, configured bool) error {
	health := "disabled"
	if configured {
		health = "configured_unverified"
	}
	return a.write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE messaging_channels SET health=?,last_observed_at=? WHERE channel_id='feishu'`, health, a.now().UnixMilli())
		return err
	})
}

func (a *App) ValidateFeishuPart(ctx context.Context, p FeishuPart) error {
	return a.store.Read(ctx, func(tx *store.ReadTx) error {
		b, err := readFeishuBinding(ctx, tx.Q())
		if err != nil {
			return err
		}
		if !b.Enabled || b.AppID != p.AppID || b.TenantKey != p.TenantKey || b.ChatID != p.ChatID || b.Revision != p.Revision {
			return protocol.Errorf(protocol.CodeForbidden, "Feishu destination binding changed")
		}
		var stale bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_bindings b JOIN followup_occurrences f ON f.occurrence_id=b.object_id JOIN followup_rules r ON r.rule_id=f.rule_id WHERE b.message_id=? AND b.object_type='followup_occurrence' AND (f.status='cancelled' OR r.status IN ('paused','cancelled')))`, p.MessageID).Scan(&stale); err != nil {
			return err
		}
		if stale {
			return protocol.Errorf(protocol.CodeInvalidTransition, "notification source is no longer current")
		}
		return nil
	})
}

// PendingFeishuInputs returns only staged inputs with no committed business
// request. Accepted/unknown requests are never automatically replayed.
func (a *App) PendingFeishuInputs(ctx context.Context) ([]FeishuIncoming, error) {
	result := []FeishuIncoming{}
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		rows, err := tx.Q().QueryContext(ctx, `SELECT i.payload FROM feishu_inputs i JOIN feishu_binding b ON b.singleton=1 AND b.enabled=1 AND b.revision=i.binding_revision AND b.owner=i.owner WHERE i.state='pending' AND NOT EXISTS(SELECT 1 FROM feishu_inputs parent WHERE parent.external_message_id=json_extract(i.payload,'$.ParentID') AND parent.owner=i.owner AND parent.binding_revision=i.binding_revision AND parent.message_id='' AND parent.state IN ('pending','accepted','unknown')) ORDER BY i.rowid LIMIT 50`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw string
			if err = rows.Scan(&raw); err != nil {
				return err
			}
			var m FeishuIncoming
			if err = json.Unmarshal([]byte(raw), &m); err != nil {
				return err
			}
			result = append(result, m)
		}
		return rows.Err()
	})
	return result, err
}

func (a *App) FeishuInputs(ctx context.Context, after int64, limit int, status string) (protocol.FeishuInputPage, error) {
	page := protocol.FeishuInputPage{Items: []protocol.FeishuInputView{}, Next: after}
	if after < 0 || limit < 1 || limit > 100 {
		return page, protocol.Errorf(protocol.CodeInvalidRequest, "invalid Feishu input pagination")
	}
	switch status {
	case "", "pending", "accepted", "unknown", "succeeded", "failed":
	default:
		return page, protocol.Errorf(protocol.CodeInvalidRequest, "invalid Feishu input status")
	}
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		rows, err := tx.Q().QueryContext(ctx, `SELECT i.rowid,i.event_id,i.external_message_id,i.request_id,i.binding_revision,i.message_id,i.state FROM feishu_inputs i WHERE i.rowid>? AND (?='' OR i.state=?) ORDER BY i.rowid LIMIT ?`, after, status, status, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v protocol.FeishuInputView
			if err = rows.Scan(&v.Sequence, &v.EventID, &v.ExternalMessageID, &v.RequestID, &v.BindingRevision, &v.MessageID, &v.Status); err != nil {
				return err
			}
			page.Items = append(page.Items, v)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		page.More = len(page.Items) > limit
		if page.More {
			page.Items = page.Items[:limit]
		}
		if len(page.Items) > 0 {
			page.Next = page.Items[len(page.Items)-1].Sequence
		}
		return nil
	})
	return page, err
}

// ObserveFeishuConnection records transport liveness, never owner authority.
func (a *App) ObserveFeishuConnection(ctx context.Context, state string) error {
	switch state {
	case "connected", "reconnecting", "disconnected", "connection_failed":
	default:
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid Feishu connection state")
	}
	return a.write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE messaging_channels SET health=?,last_observed_at=? WHERE channel_id='feishu' AND health<>'disabled'`, state, a.now().UnixMilli())
		return err
	})
}
