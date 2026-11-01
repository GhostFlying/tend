package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

const MessagingInteractionDestination = "messaging:interaction"

var interactionLabels = map[string]string{
	"retry the implementation": "重新实现", "raise the budget": "提高预算", "retry acceptance": "重新验收",
	"revise the spec": "修改要求", "fix the acceptance environment": "修好验收环境", "cancel the task": "取消任务",
	"wait for the host": "等待主机恢复", "start over on another host": "换主机重新开始",
}

func interactionLabel(option string) string {
	if s := interactionLabels[option]; s != "" {
		return s
	}
	return option
}

// bindMessagingInteraction freezes the question the recipient actually sees.
// Its current state is always read from the original business object.
func bindMessagingInteraction(ctx context.Context, x *Exec, tx *store.Tx, message string) error {
	var kind, id string
	err := tx.Q().QueryRowContext(ctx, `SELECT object_type,object_id FROM conversation_bindings WHERE message_id=? AND object_type IN ('decision','attention','work_item_choice') ORDER BY CASE object_type WHEN 'decision' THEN 0 WHEN 'attention' THEN 1 ELSE 2 END LIMIT 1`, message).Scan(&kind, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	i := protocol.MessagingInteraction{ObjectType: kind, ObjectID: id, TextAllowed: true, State: "open", Options: []protocol.MessagingOption{}}
	if kind == "attention" {
		a, e := store.GetAttention(ctx, tx.Q(), id)
		if e != nil {
			return e
		}
		if a.Kind == "information" {
			return nil
		}
		if a.DecisionID != "" {
			kind, id = "decision", a.DecisionID
		} else if a.ChoiceID != "" {
			kind, id = "work_item_choice", a.ChoiceID
		} else {
			if a.Kind != "action" || a.Status != "open" {
				return nil
			}
			i.Question, i.Version, i.IssueID, i.TaskID = a.Title, a.Revision, a.IssueID, a.TaskID
		}
	}
	i.ObjectType, i.ObjectID = kind, id
	switch kind {
	case "decision":
		d, e := store.GetDecision(ctx, tx.Q(), id)
		if e != nil {
			return e
		}
		if d.Status != "open" {
			return nil
		}
		i.Question, i.Version, i.TaskID = d.Question, d.Version, d.TaskID
		if d.Worker == nil {
			original, err := store.GetMessage(ctx, tx.Q(), message)
			if err != nil {
				return err
			}
			if _, extra, found := strings.Cut(original.Body, "\n\n"+d.Question); found {
				i.Context = strings.TrimSpace(extra)
			}
		}
		if d.Worker != nil {
			i.Context = d.Worker.Evidence
			for _, issue := range d.Worker.Issues {
				i.Context = strings.TrimSpace(i.Context + "\n" + issue.Summary + "\n" + issue.Impact + "\n" + issue.NextAction)
			}
		}
		var options []string
		var effects map[string]protocol.DecisionEffect
		if e = json.Unmarshal([]byte(d.OptionsJSON), &options); e != nil {
			return e
		}
		if d.EffectsJSON != "" {
			if e = json.Unmarshal([]byte(d.EffectsJSON), &effects); e != nil {
				return e
			}
		}
		for _, o := range options {
			i.Options = append(i.Options, protocol.MessagingOption{ID: o, Label: interactionLabel(o), Effect: effects[o]})
		}
		if e := tx.Q().QueryRowContext(ctx, `SELECT COALESCE(issue_id,'') FROM attentions WHERE decision_id=?`, id).Scan(&i.IssueID); e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
	case "work_item_choice":
		c, e := store.GetWorkItemChoice(ctx, tx.Q(), id)
		if e != nil {
			return e
		}
		if c.Status != "open" {
			return nil
		}
		i.Question, i.Version, i.IssueID = c.Question, c.Version, c.WorkItemID
		for _, o := range c.Options {
			i.Options = append(i.Options, protocol.MessagingOption{ID: o, Label: o})
		}
	}
	if kind == "attention" {
		var existing string
		e := tx.Q().QueryRowContext(ctx, `SELECT interaction_id FROM messaging_interactions WHERE object_type='attention' AND object_id=? AND json_extract(snapshot_json,'$.question')=? AND observed_state='open' LIMIT 1`, id, i.Question).Scan(&existing)
		if e == nil {
			_, e = tx.Exec(ctx, `INSERT INTO messaging_interaction_messages(message_id,interaction_id) VALUES(?,?) ON CONFLICT(message_id) DO NOTHING`, message, existing)
			return e
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
	}
	i.ID = x.IDs.New(protocol.Prefix("int"))
	raw, e := json.Marshal(i)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO messaging_interactions(interaction_id,object_type,object_id,question_version,snapshot_json) VALUES(?,?,?,?,?) ON CONFLICT(object_type,object_id,question_version) DO NOTHING`, i.ID, kind, id, i.Version, string(raw)); e != nil {
		return e
	}
	if e = tx.Q().QueryRowContext(ctx, `SELECT interaction_id FROM messaging_interactions WHERE object_type=? AND object_id=? AND question_version=?`, kind, id, i.Version).Scan(&i.ID); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO messaging_interaction_messages(message_id,interaction_id) VALUES(?,?) ON CONFLICT(message_id) DO NOTHING`, message, i.ID)
	return e
}

func messagingInteraction(ctx context.Context, q store.Querier, id string) (protocol.MessagingInteraction, error) {
	var i protocol.MessagingInteraction
	var raw, observed string
	err := q.QueryRowContext(ctx, `SELECT snapshot_json,handled_channel,handled_at,answer_summary,observed_state FROM messaging_interactions WHERE interaction_id=?`, id).Scan(&raw, &i.HandledChannel, &i.HandledAt, &i.AnswerSummary, &observed)
	if err != nil {
		return i, errNotFound(err, "交互请求不存在")
	}
	channel, at, summary := i.HandledChannel, i.HandledAt, i.AnswerSummary
	if err = json.Unmarshal([]byte(raw), &i); err != nil {
		return i, err
	}
	i.HandledChannel, i.HandledAt, i.AnswerSummary = channel, at, summary
	if observed == "superseded" {
		i.State = observed
		return i, nil
	}
	switch i.ObjectType {
	case "decision":
		d, e := store.GetDecision(ctx, q, i.ObjectID)
		if e != nil {
			return i, e
		}
		switch {
		case d.Status == "answered" && d.Version == i.Version+1:
			i.State = "answered"
			var a struct {
				Option string                 `json:"option"`
				Text   string                 `json:"text"`
				Effect *protocol.EffectResult `json:"effect"`
			}
			if e = json.Unmarshal([]byte(d.AnswerJSON), &a); e != nil {
				return i, e
			}
			i.Effect = a.Effect
			if i.AnswerSummary == "" {
				i.AnswerSummary = strings.TrimSpace(interactionLabel(a.Option) + " " + a.Text)
			}
		case d.Version > i.Version+1:
			i.State = "superseded"
		case d.Status != "open":
			i.State = "withdrawn"
		case d.Version != i.Version:
			i.State = "superseded"
		default:
			i.State = "open"
		}
	case "attention":
		a, e := store.GetAttention(ctx, q, i.ObjectID)
		if e != nil {
			return i, e
		}
		switch {
		case a.Title != i.Question:
			i.State = "superseded"
		case a.Status == "resolved":
			i.State = "answered"
			if i.AnswerSummary == "" {
				i.AnswerSummary = a.Resolution
			}
		case a.Status != "open":
			i.State = "withdrawn"
		default:
			i.State = "open"
		}
	case "work_item_choice":
		c, e := store.GetWorkItemChoice(ctx, q, i.ObjectID)
		if e != nil {
			return i, e
		}
		switch {
		case c.Status == "answered" && c.Version == i.Version+1:
			i.State = "answered"
			if i.AnswerSummary == "" {
				i.AnswerSummary = c.Answer
			}
		case c.Status != "open":
			i.State = "withdrawn"
		case c.Version != i.Version:
			i.State = "superseded"
		default:
			i.State = "open"
		}
	}
	return i, nil
}
func interactionForMessage(ctx context.Context, q store.Querier, message string) (*protocol.MessagingInteraction, error) {
	var id string
	err := q.QueryRowContext(ctx, `SELECT interaction_id FROM messaging_interaction_messages WHERE message_id=?`, message).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	i, err := messagingInteraction(ctx, q, id)
	return &i, err
}
func (a *App) MessagingInteraction(ctx context.Context, id string) (protocol.MessagingInteraction, error) {
	var i protocol.MessagingInteraction
	err := a.store.Read(ctx, func(tx *store.ReadTx) error { var e error; i, e = messagingInteraction(ctx, tx.Q(), id); return e })
	return i, err
}

// RespondMessagingInteraction admits only a frozen action, never a caller supplied command.
type RespondMessagingInteraction struct{}

func (RespondMessagingInteraction) Operation() string { return "messaging.respond" }
func (RespondMessagingInteraction) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (RespondMessagingInteraction) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.RespondInteraction
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	return respondMessagingInteraction(ctx, x, tx, x.Env.Target.ID, p, &communicationOrigin{Channel: "web"})
}
func respondMessagingInteraction(ctx context.Context, x *Exec, tx *store.Tx, id string, p protocol.RespondInteraction, origin *communicationOrigin) (Outcome, error) {
	i, err := messagingInteraction(ctx, tx.Q(), id)
	if err != nil {
		return Outcome{}, err
	}
	if p.Version != i.Version {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "问题版本已变化，请重新阅读")
	}
	if i.State != "open" {
		var message string
		if err = tx.Q().QueryRowContext(ctx, `SELECT message_id FROM messaging_interaction_messages WHERE interaction_id=? ORDER BY rowid LIMIT 1`, id).Scan(&message); err != nil {
			return Outcome{}, err
		}
		if err = queueInteractionUpdates(ctx, x, tx, message); err != nil {
			return Outcome{}, err
		}
		if err = communicationEvent(ctx, x, tx, message, "messaging.interaction_changed"); err != nil {
			return Outcome{}, err
		}
		return Outcome{Result: map[string]any{"interaction": i, "already_handled": true, "message_id": message}, Notify: true}, nil
	}
	if len(p.Text) > maxMessageBytes || strings.TrimSpace(p.Text) == "" && p.Option == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "请选择或填写回答")
	}
	var selected *protocol.MessagingOption
	for _, o := range i.Options {
		if o.ID == p.Option {
			v := o
			selected = &v
			break
		}
	}
	if selected != nil && origin.RawBody == "" {
		origin.RawBody = selected.Label
	}
	if p.Option != "" && selected == nil {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "回答不属于原问题选项")
	}
	var message string
	if err = tx.Q().QueryRowContext(ctx, `SELECT message_id FROM messaging_interaction_messages WHERE interaction_id=? ORDER BY rowid LIMIT 1`, id).Scan(&message); err != nil {
		return Outcome{}, err
	}
	if origin.ReplyTo == "" {
		origin.ReplyTo = message
	}
	// Extra requirements must remain an admitted source, not silently execute an old effect.
	worker := false
	if i.ObjectType == "decision" {
		d, e := store.GetDecision(ctx, tx.Q(), i.ObjectID)
		if e != nil {
			return Outcome{}, e
		}
		worker = d.Worker != nil
	}
	if (i.ObjectType == "attention" && !p.Confirm) || (i.ObjectType != "attention" && (!worker && (p.Option == "" || p.Text != ""))) {
		body := p.Text
		if p.Option != "" {
			body = interactionLabel(p.Option) + "\n\n" + p.Text
		}
		return submitCommunicationInput(ctx, x, tx, protocol.MessagingInput{Body: body, ReplyTo: message}, origin.Channel, origin.BotID, origin.ChatID, origin.ExternalID, false)
	}
	inner := *x
	inner.interactionOrigin = origin
	env := *x.Env
	inner.Env = &env
	var out Outcome
	switch i.ObjectType {
	case "decision":
		env.Target = protocol.Target{Type: "decision", ID: i.ObjectID}
		env.Payload, _ = json.Marshal(protocol.AnswerDecisionPayload{Version: i.Version, Option: p.Option, Text: p.Text})
		out, err = (AnswerDecision{}).Apply(ctx, &inner, tx)
		if err == nil {
			d, e := store.GetDecision(ctx, tx.Q(), i.ObjectID)
			err = e
			message = d.AnsweredMessageID
		}
	case "attention":
		if strings.TrimSpace(p.Text) == "" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "请明确说明已完成的事项")
		}
		message, err = interactionAnswerSource(ctx, x, tx, message, p.Text, origin)
		if err != nil {
			return Outcome{}, err
		}
		env.Target = protocol.Target{Type: "attention", ID: i.ObjectID}
		a, e := store.GetAttention(ctx, tx.Q(), i.ObjectID)
		if e != nil {
			return Outcome{}, e
		}
		env.ExpectedControlRevision = &a.Revision
		env.Payload, _ = json.Marshal(protocol.ConfirmAttentionPayload{Summary: p.Text, SourceMessageID: message})
		out, err = (AttentionCommand{Action: "confirm"}).Apply(ctx, &inner, tx)
	case "work_item_choice":
		message, err = interactionAnswerSource(ctx, x, tx, message, interactionLabel(p.Option), origin)
		if err != nil {
			return Outcome{}, err
		}
		env.Target = protocol.Target{Type: "work_item_choice", ID: i.ObjectID}
		env.ExpectedControlRevision = &i.Version
		env.Payload, _ = json.Marshal(protocol.UpdateWorkItemChoicePayload{Answer: p.Option, OwnerMessageID: message})
		out, err = (UpdateWorkItemChoice{Action: "respond", FromMessage: true}).Apply(ctx, &inner, tx)
	}
	if err != nil {
		return Outcome{}, err
	}

	if _, err = tx.Exec(ctx, `UPDATE messaging_interactions SET handled_channel=?,handled_at=?,response_message_id=? WHERE interaction_id=? AND handled_at=0`, origin.Channel, x.Now.UnixMilli(), message, id); err != nil {
		return Outcome{}, err
	}
	i, err = messagingInteraction(ctx, tx.Q(), id)
	if err != nil {
		return Outcome{}, err
	}
	out.Result = map[string]any{"interaction": i, "message_id": message}
	out.Events = append(out.Events, Event{ObjectType: "message", ObjectID: message, Kind: "conversation.message_state", Body: map[string]any{"state": "replied", "interaction_id": i.ID}})
	out.Notify = true
	// A formal reply is delivered only to the channel where this answer originated.
	summary := splitCommunication(i.AnswerSummary, 1200)
	answer := ""
	if len(summary) > 0 {
		answer = summary[0]
	}
	if len(summary) > 1 {
		answer += "…（完整回答保留在原消息中）"
	}
	pReply := protocol.MessagingPublication{Body: "已记录你的回答：" + answer, Purpose: "final", ReplyTo: message}
	if i.Effect != nil {
		if i.Effect.Applied {
			pReply.Body += "。已应用所选操作；后续执行结果会继续反馈。"
		} else {
			pReply.Body += "。操作未应用：" + i.Effect.Reason
		}
	}
	publication, err := publishCommunication(ctx, x, tx, pReply, "", "main", "system")
	if err != nil {
		return Outcome{}, err
	}
	out.Events = append(out.Events, publication.Events...)
	return out, nil
}
func interactionAnswerSource(ctx context.Context, x *Exec, tx *store.Tx, reply, body string, origin *communicationOrigin) (string, error) {
	if origin.RawBody != "" {
		body = origin.RawBody
	}
	id := x.IDs.New(protocol.PrefixMessage)
	seq, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: id, Author: "owner", Body: body, ReplyTo: reply, RequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli()})
	if err != nil {
		return "", err
	}
	if _, err = tx.InsertEvent(ctx, store.EventRow{EventID: x.IDs.New(protocol.PrefixEvent), Source: "app", ObjectType: "message", ObjectID: id, ObjectVersion: seq, Kind: "conversation.message", BodyJSON: `{"author":"owner"}`, RequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli()}); err != nil {
		return "", err
	}
	child := *x
	child.interactionOrigin = origin
	if err := registerInteractionAnswerInput(ctx, &child, tx, id, reply); err != nil {
		return "", err
	}
	return id, nil
}

func submitCommunication(ctx context.Context, x *Exec, tx *store.Tx, p protocol.MessagingInput, channel string, bot, chat, external int64, externalEvidence bool) (Outcome, error) {
	if p.ReplyTo != "" && p.CorrectionOf == "" && !externalEvidence && len(p.Attachments) == 0 {
		i, err := interactionForMessage(ctx, tx.Q(), p.ReplyTo)
		if err != nil {
			return Outcome{}, err
		}
		if i != nil {
			origin := &communicationOrigin{Channel: channel, BotID: bot, ChatID: chat, ExternalID: external, ReplyTo: p.ReplyTo, RawBody: p.Body}
			if i.State != "open" && channel == "telegram" {
				source, err := interactionAnswerSource(ctx, x, tx, p.ReplyTo, p.Body, origin)
				if err != nil {
					return Outcome{}, err
				}
				notice, err := publishCommunication(ctx, x, tx, protocol.MessagingPublication{Body: boundedInteractionNotice(*i) + "\n\n已保留你的补充文字。原问题不再接受回答；如需继续讨论，请另发新消息。", Purpose: "final", ReplyTo: source}, "", "main", "system")
				if err != nil {
					return Outcome{}, err
				}
				notice.Result = map[string]any{"message_id": source, "interaction": i, "already_handled": true}
				notice.Events = append(notice.Events, Event{ObjectType: "message", ObjectID: source, Kind: "conversation.message_state", Body: map[string]any{"state": "replied", "interaction_id": i.ID, "late_reply": true}})
				notice.Notify = true
				return notice, nil
			}
			response := protocol.RespondInteraction{Version: i.Version, Text: p.Body}
			if i.ObjectType == "attention" && strings.HasPrefix(strings.TrimSpace(p.Body), "已完成：") {
				response.Confirm = true
				response.Text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(p.Body), "已完成："))
			}
			matches := 0
			for _, o := range i.Options {
				if strings.TrimSpace(p.Body) == o.ID || strings.TrimSpace(p.Body) == o.Label {
					matches++
					response.Option = o.ID
				}
			}
			if n, e := strconv.Atoi(strings.TrimSpace(p.Body)); matches == 0 && e == nil && n > 0 && n <= len(i.Options) {
				matches = 1
				response.Option = i.Options[n-1].ID
			}
			if matches == 1 {
				response.Text = ""
			} else {
				response.Option = ""
			}
			return respondMessagingInteraction(ctx, x, tx, i.ID, response, origin)
		}
	}
	return submitCommunicationInput(ctx, x, tx, p, channel, bot, chat, external, externalEvidence)
}

// syncMessagingInteractions observes original business changes inside their transaction.
func syncMessagingInteractions(ctx context.Context, x *Exec, tx *store.Tx, events []Event) error {
	ids := map[string]bool{}
	for _, e := range events {
		if e.ObjectType != "decision" && e.ObjectType != "attention" && e.ObjectType != "work_item_choice" {
			continue
		}
		found, err := store.FollowupIDs(ctx, tx.Q(), `SELECT interaction_id FROM messaging_interactions WHERE (object_type=? AND object_id=?) OR (?='attention' AND ((object_type='decision' AND object_id=(SELECT decision_id FROM attentions WHERE attention_id=?)) OR (object_type='work_item_choice' AND object_id=(SELECT choice_id FROM attentions WHERE attention_id=?))))`, e.ObjectType, e.ObjectID, e.ObjectType, e.ObjectID, e.ObjectID)
		if err != nil {
			return err
		}
		for _, id := range found {
			ids[id] = true
		}
	}
	for id := range ids {
		i, err := messagingInteraction(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		var observed string
		if err = tx.Q().QueryRowContext(ctx, `SELECT observed_state FROM messaging_interactions WHERE interaction_id=?`, id).Scan(&observed); err != nil {
			return err
		}
		if observed == i.State {
			continue
		}
		if i.State == "answered" && i.HandledAt == 0 {
			source := ""
			switch i.ObjectType {
			case "decision":
				d, e := store.GetDecision(ctx, tx.Q(), i.ObjectID)
				if e != nil {
					return e
				}
				source = d.AnsweredMessageID
			case "attention":
				a, e := store.GetAttention(ctx, tx.Q(), i.ObjectID)
				if e != nil {
					return e
				}
				source = a.ResolutionSourceID
			}
			channel := ""
			if source != "" {
				if e := tx.Q().QueryRowContext(ctx, `SELECT channel_id FROM messaging_messages WHERE message_id=?`, source).Scan(&channel); e != nil && !errors.Is(e, sql.ErrNoRows) {
					return e
				}
			}
			if _, err = tx.Exec(ctx, `UPDATE messaging_interactions SET handled_channel=?,handled_at=?,response_message_id=? WHERE interaction_id=? AND handled_at=0`, channel, x.Now.UnixMilli(), source, id); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE messaging_interactions SET observed_state=? WHERE interaction_id=?`, i.State, id); err != nil {
			return err
		}
		messages, err := store.FollowupIDs(ctx, tx.Q(), `SELECT message_id FROM messaging_interaction_messages WHERE interaction_id=?`, id)
		if err != nil {
			return err
		}
		for _, message := range messages {
			if err = communicationEvent(ctx, x, tx, message, "messaging.interaction_changed"); err != nil {
				return err
			}
			if err = queueInteractionUpdates(ctx, x, tx, message); err != nil {
				return err
			}
		}
	}
	return nil
}

func interactionText(i protocol.MessagingInteraction) string {
	body := i.Question
	if i.Context != "" {
		body += "\n\n依据：" + i.Context
	}
	if i.State == "open" {
		for pos, o := range i.Options {
			body += "\n" + strconv.Itoa(pos+1) + ". " + o.Label
		}
		if i.ObjectType == "attention" {
			body += "\n确认完成请引用本消息回复：已完成：具体完成说明。其他文字会交负责人处理。"
		} else {
			body += "\n也可以引用本消息，输入回答或补充说明。"
		}
	} else {
		question := body
		body = ""
		switch i.State {
		case "answered":
			via := ""
			if i.HandledChannel == "web" {
				via = " Web"
			} else if i.HandledChannel == "telegram" {
				via = " Telegram"
			}
			if via != "" {
				body += "\n\n已在" + via + " 处理"
			} else {
				body += "\n\n已回答"
			}
			if i.AnswerSummary != "" {
				body += "：" + i.AnswerSummary
			}
			if i.HandledAt > 0 {
				body += " · " + time.UnixMilli(i.HandledAt).UTC().Format("15:04 UTC")
			}
		case "superseded":
			body += "\n\n问题已更新，此版本不再接受回答。"
		default:
			body += "\n\n此请求已撤回或不再适用。"
		}
		body = strings.TrimSpace(body) + "\n\n" + question
	}
	return body
}
func boundedInteractionNotice(i protocol.MessagingInteraction) string {
	text := splitCommunication(interactionText(i), 3800)
	if len(text) == 0 {
		return ""
	}
	result := text[0]
	if len(text) > 1 {
		result += "\n…（完整内容可在 Web 查看）"
	}
	return result
}
func queueInteractionUpdates(ctx context.Context, x *Exec, tx *store.Tx, message string) error {
	rows, err := tx.Q().QueryContext(ctx, `SELECT external_id,bot_id,chat_id,binding_revision FROM messaging_deliveries WHERE message_id=? AND channel_id='telegram' AND kind='text' AND part=0 AND state='accepted' AND external_id<>0`, message)
	if err != nil {
		return err
	}
	var parts []MessagingPart
	for rows.Next() {
		p := MessagingPart{MessageID: message, Kind: "interaction"}
		if err = rows.Scan(&p.ReplyExternalID, &p.BotID, &p.ChatID, &p.BindingRevision); err != nil {
			rows.Close()
			return err
		}
		parts = append(parts, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	i, err := interactionForMessage(ctx, tx.Q(), message)
	if err != nil || i == nil {
		return err
	}
	for _, p := range parts {
		p.Body = interactionText(*i)
		var queued bool
		if err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messaging_deliveries WHERE message_id=? AND kind='interaction' AND bot_id=? AND chat_id=? AND binding_revision=? AND reply_external_id=? AND body=?)`, message, p.BotID, p.ChatID, p.BindingRevision, p.ReplyExternalID, p.Body).Scan(&queued); err != nil {
			return err
		}
		if queued {
			continue
		}
		if err = queueInteractionPart(ctx, x, tx, p, MessagingInteractionDestination); err != nil {
			return err
		}
	}
	return nil
}
func queueInteractionPart(ctx context.Context, x *Exec, tx *store.Tx, p MessagingPart, destination string) error {
	p.DeliveryID = x.IDs.New(protocol.PrefixOutbox)
	var part int64
	if err := tx.Q().QueryRowContext(ctx, `SELECT min(COALESCE(min(part),-1),-1)-1 FROM messaging_deliveries WHERE message_id=? AND channel_id='telegram'`, p.MessageID).Scan(&part); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO messaging_deliveries(delivery_id,message_id,channel_id,part,kind,bot_id,chat_id,reply_external_id,body,state,created_at,updated_at,binding_revision) VALUES(?,?,'telegram',?,?,?,?,?,?,'queued',?,?,?)`, p.DeliveryID, p.MessageID, part, p.Kind, p.BotID, p.ChatID, p.ReplyExternalID, p.Body, x.Now.UnixMilli(), x.Now.UnixMilli(), p.BindingRevision); err != nil {
		return err
	}
	p.Part = part
	return insertMessagingOutbox(ctx, x, tx, p, destination)
}

// InitializeMessagingInteractions publishes current open questions once after
// upgrading. It does not invent old question versions or historical receipts.
func (a *App) InitializeMessagingInteractions(ctx context.Context) error {
	env := &protocol.Envelope{RequestID: a.ids.New(protocol.PrefixRequest), Operation: "messaging.initialize_interactions", Target: protocol.Target{Type: "system"}, Payload: json.RawMessage(`{}`)}
	_, err := a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, env, initializeMessagingInteractions{})
	return err
}

type initializeMessagingInteractions struct{}

func (initializeMessagingInteractions) Operation() string { return "messaging.initialize_interactions" }
func (initializeMessagingInteractions) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}
func (initializeMessagingInteractions) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	rows, err := tx.Q().QueryContext(ctx, `SELECT 'decision',decision_id,question FROM decisions d WHERE status='open' AND NOT EXISTS(SELECT 1 FROM messaging_interactions i WHERE i.object_type='decision' AND i.object_id=d.decision_id AND i.question_version=d.version) UNION ALL SELECT 'attention',attention_id,json_extract(body_json,'$.title') FROM attentions a WHERE kind='action' AND status='open' AND NOT EXISTS(SELECT 1 FROM messaging_interactions i WHERE i.object_type='attention' AND i.object_id=a.attention_id AND json_extract(i.snapshot_json,'$.question')=json_extract(a.body_json,'$.title') AND i.observed_state='open')`)
	if err != nil {
		return Outcome{}, err
	}
	type question struct{ kind, id, body string }
	var questions []question
	for rows.Next() {
		var q question
		if err = rows.Scan(&q.kind, &q.id, &q.body); err != nil {
			rows.Close()
			return Outcome{}, err
		}
		questions = append(questions, q)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Outcome{}, err
	}
	if len(questions) == 0 {
		return Outcome{NoEffect: true}, nil
	}
	for _, q := range questions {
		message := x.IDs.New(protocol.PrefixMessage)
		if _, err = tx.InsertMessage(ctx, store.MessageRow{MessageID: message, Author: "system", Body: q.body, RequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli()}); err != nil {
			return Outcome{}, err
		}
		if err = tx.BindMessage(ctx, message, q.kind, q.id, "interaction"); err != nil {
			return Outcome{}, err
		}
		if err = registerSystemCommunication(ctx, x, tx, message, "interaction_current:"+q.id); err != nil {
			return Outcome{}, err
		}
	}
	return Outcome{Notify: true}, nil
}

func (a *App) MessagingInteractionForMessage(ctx context.Context, message string) (*protocol.MessagingInteraction, error) {
	var i *protocol.MessagingInteraction
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var e error
		i, e = interactionForMessage(ctx, tx.Q(), message)
		return e
	})
	return i, err
}
func MessagingInteractionText(i protocol.MessagingInteraction) string { return interactionText(i) }

type telegramInteraction struct {
	Inbox                TelegramInbox
	User, Chat, External int64
	Data                 string
}

func (telegramInteraction) Operation() string { return "messaging.telegram_interaction" }
func (c telegramInteraction) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	if err := allowKinds(x.Principal, protocol.PrincipalOwner); err != nil {
		return err
	}
	var allowed bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messaging_channels WHERE channel_id='telegram' AND status='ready' AND bot_id=? AND user_id=? AND chat_id=? AND owner=?)`, c.Inbox.BotID, c.User, c.Chat, x.Principal.Subject).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return protocol.Errorf(protocol.CodeForbidden, "Telegram 绑定已失效")
	}
	return nil
}
func (c telegramInteraction) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	parts := strings.Split(c.Data, ":")
	if len(parts) != 3 || parts[0] != "t" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "无效的消息操作")
	}
	i, err := messagingInteraction(ctx, tx.Q(), parts[1])
	if err != nil {
		return Outcome{}, err
	}
	var message string
	err = tx.Q().QueryRowContext(ctx, `SELECT d.message_id FROM messaging_deliveries d JOIN messaging_interaction_messages m USING(message_id) JOIN messaging_channels c ON c.channel_id='telegram' WHERE d.bot_id=? AND d.chat_id=? AND d.external_id=? AND d.binding_revision=c.revision AND m.interaction_id=? LIMIT 1`, c.Inbox.BotID, c.Chat, c.External, i.ID).Scan(&message)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Outcome{}, protocol.Errorf(protocol.CodeNotFound, "操作不属于这条问题")
		}
		return Outcome{}, err
	}
	var out Outcome
	if parts[2] == "text" && i.State != "open" {
		out, err = respondMessagingInteraction(ctx, x, tx, i.ID, protocol.RespondInteraction{Version: i.Version, Text: ""}, &communicationOrigin{Channel: "telegram", BotID: c.Inbox.BotID, ChatID: c.Chat, ReplyTo: message})
	} else if parts[2] == "text" {
		id := x.IDs.New(protocol.PrefixMessage)
		body := i.Question + "\n\n请回复这条消息，输入你的回答或补充说明。"
		if i.ObjectType == "attention" {
			body += "\n确认完成请回复：已完成：具体完成说明"
		}
		conversation, routeErr := communicationRoute(ctx, x, tx, message)
		if routeErr != nil {
			return Outcome{}, routeErr
		}
		_, err = tx.InsertMessage(ctx, store.MessageRow{MessageID: id, ConversationID: conversation, Author: "system", Body: body, ReplyTo: message, RequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli()})
		if err == nil {
			err = tx.BindMessage(ctx, id, i.ObjectType, i.ObjectID, "interaction")
		}
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO messaging_messages(message_id,direction,channel_id,reply_to,purpose,reason) VALUES(?,'publication','telegram',?,'progress','interaction_text')`, id, message)
		}
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO messaging_interaction_messages(message_id,interaction_id) VALUES(?,?)`, id, i.ID)
		}
		if err == nil {
			err = queueCommunication(ctx, x, tx, id, "telegram", c.Inbox.BotID, c.Chat, c.External, protocol.MessagingPublication{Body: body, Purpose: "interaction_prompt"})
		}
		if err == nil {
			err = communicationEvent(ctx, x, tx, id, "messaging.published")
		}
		out = Outcome{Notify: true, Result: map[string]any{"message_id": id}}
	} else {
		index, e := strconv.Atoi(parts[2])
		if e != nil || index < 0 || index >= len(i.Options) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "无效的问题选项")
		}
		out, err = respondMessagingInteraction(ctx, x, tx, i.ID, protocol.RespondInteraction{Version: i.Version, Option: i.Options[index].ID}, &communicationOrigin{Channel: "telegram", BotID: c.Inbox.BotID, ChatID: c.Chat, ReplyTo: message})
	}
	if err != nil {
		return Outcome{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE telegram_inbox SET state='processed' WHERE bot_id=? AND update_id=?`, c.Inbox.BotID, c.Inbox.UpdateID)
	return out, err
}
func (a *App) RespondTelegramInteraction(ctx context.Context, u TelegramInbox, user, chat, external int64, data string) (protocol.Operation, error) {
	b, err := a.TelegramBinding(ctx)
	if err != nil {
		return protocol.Operation{}, err
	}
	payload, _ := json.Marshal(map[string]any{"user": user, "chat": chat, "message": external, "data": data})
	env := &protocol.Envelope{RequestID: u.RequestID, Operation: "messaging.telegram_interaction", Target: protocol.Target{Type: "message"}, Payload: payload}
	return a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalOwner, Subject: b.Owner, Client: "telegram"}, env, telegramInteraction{Inbox: u, User: user, Chat: chat, External: external, Data: data})
}

func registerInteractionAnswerInput(ctx context.Context, x *Exec, tx *store.Tx, id, reply string) error {
	o := x.interactionOrigin
	if o == nil {
		return registerWebCommunicationInput(ctx, tx, id, reply)
	}
	if o.ReplyTo != "" {
		reply = o.ReplyTo
	}
	_, err := tx.Exec(ctx, `INSERT INTO messaging_messages(message_id,direction,channel_id,reply_to,source_revision,bot_id,chat_id,external_id) SELECT d.message_id,'input',?,?,d.content_revision,?,?,? FROM message_dispositions d WHERE d.message_id=?`, o.Channel, reply, o.BotID, o.ChatID, o.ExternalID, id)
	return err
}
