package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

const MessagingDestination = "messaging:telegram"
const MessagingReadDestination = "messaging:read"

// MessagingCommand exposes owner communication without granting agent identity.
type MessagingCommand struct{ Action string }

func (c MessagingCommand) Operation() string { return "messaging." + c.Action }
func (MessagingCommand) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (c MessagingCommand) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	switch c.Action {
	case "send", "reply":
		var p protocol.MessagingInput
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		if c.Action == "reply" {
			if p.ReplyTo != "" && p.ReplyTo != x.Env.Target.ID {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "payload reply target conflicts with explicit target")
			}
			p.ReplyTo = x.Env.Target.ID
		}
		return submitCommunication(ctx, x, tx, p, "web", 0, 0, 0, false)
	case "notify":
		var p protocol.MessagingPublication
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		p.ReplyTo = ""
		return publishCommunication(ctx, x, tx, p, "", "main", "owner")
	case "read":
		var p protocol.MessagingReadPayload
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		if p.ContentRevision < 1 {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "content_revision is required")
		}
		if err := recordCommunicationRead(ctx, x, tx, x.Env.Target.ID, p.ContentRevision, "owner", "web", "explicit_owner_read", ""); err != nil {
			return Outcome{}, err
		}
		return Outcome{Result: map[string]any{"message_id": x.Env.Target.ID}, Notify: true}, nil
	case "bind", "unbind":
		expected, err := requireRevision(x.Env)
		if err != nil {
			return Outcome{}, err
		}
		var p protocol.BindTelegram
		if c.Action == "bind" {
			if err = decodePayload(x.Env, &p); err != nil {
				return Outcome{}, err
			}
			if p.UserID <= 0 {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "positive user_id is required")
			}
		}
		status := "waiting_start"
		if c.Action == "unbind" {
			status = "disabled"
		}
		res, err := tx.Exec(ctx, `UPDATE messaging_channels SET owner=?,user_id=?,chat_id=0,status=?,revision=revision+1,updated_at=? WHERE channel_id='telegram' AND revision=?`, x.Principal.Subject, p.UserID, status, x.Now.UnixMilli(), expected)
		if err != nil {
			return Outcome{}, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return Outcome{}, err
		}
		if n != 1 {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "channel binding changed")
		}
		return Outcome{Result: map[string]any{"channel_id": "telegram", "status": status}, Events: []Event{{ObjectType: "channel", ObjectID: "telegram", Kind: "messaging.channel_changed"}}}, nil
	}
	return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unknown messaging action")
}

func communicationEvent(ctx context.Context, x *Exec, tx *store.Tx, id, kind string) error {
	_, err := tx.InsertEvent(ctx, store.EventRow{EventID: x.IDs.New(protocol.PrefixEvent), Source: "messaging", ObjectType: "message", ObjectID: id, Kind: kind, BodyJSON: `{}`, RequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli()})
	return err
}

func communicationRoute(ctx context.Context, x *Exec, tx *store.Tx, reply string) (string, error) {
	if reply == "" {
		return "main", nil
	}
	q := tx.Q()
	var conversation string
	err := q.QueryRowContext(ctx, `SELECT c.conversation_id FROM messaging_messages m JOIN conversation_messages c USING(message_id) WHERE m.message_id=?`, reply).Scan(&conversation)
	if err != nil {
		return "", errNotFound(err, "reply target is not a communication message")
	}
	// Business ownership follows the current Issue, not the original executor.
	issues, err := communicationIssues(ctx, q, reply)
	if err != nil {
		return "", err
	}

	if len(issues) > 1 {
		return "", protocol.Errorf(protocol.CodeInvalidRequest, "quoted message has multiple Issue responsibilities")
	}
	if len(issues) == 1 {
		issue, err := store.GetCanonicalIssue(ctx, q, issues[0])
		if err == nil {
			return ensureCanonicalManagerConversation(ctx, tx, issue, x.Now.UnixMilli())
		}
		if !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
	}

	return conversation, nil
}

func communicationIssues(ctx context.Context, q store.Querier, id string) ([]string, error) {
	return store.FollowupIDs(ctx, q, `SELECT work_item_id FROM messaging_issue_links WHERE message_id=? ORDER BY work_item_id`, id)
}

// Display publications add no technical evidence. Retain the named planner's
// or manager's Issue association without invalidating a manager's source review.
func bindCommunicationIssue(ctx context.Context, x *Exec, tx *store.Tx, id, issue string) error {
	if x.coordinationExecution != nil && (x.coordinationExecution.Selection.Purpose == agentconfig.Main || x.coordinationExecution.IssueID == issue) {
		if _, err := tx.Exec(ctx, `INSERT OR IGNORE INTO message_work_items(message_id,work_item_id,source,updated_at) VALUES(?,?,'conversation',?)`, id, issue, x.Now.UnixMilli()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM message_context_obligations WHERE message_id=? AND work_item_id=?`, id, issue); err != nil {
			return err
		}
	}
	return tx.BindMessage(ctx, id, "work_item", issue, "publication")
}

// communicationOrigin is populated only by Tend's local channel adapters.
type communicationOrigin struct {
	Channel                   string
	BotID, ChatID, ExternalID int64
	ReplyTo, CorrectionOf     string
}

func submitCommunication(ctx context.Context, x *Exec, tx *store.Tx, p protocol.MessagingInput, channel string, bot, chat, external int64, externalEvidence bool) (Outcome, error) {
	conversation, err := communicationRoute(ctx, x, tx, p.ReplyTo)
	if err != nil {
		return Outcome{}, err
	}
	if p.CorrectionOf != "" {
		var prior string
		if err := tx.Q().QueryRowContext(ctx, `SELECT c.conversation_id FROM messaging_messages m JOIN conversation_messages c USING(message_id) WHERE m.message_id=? AND m.direction='input' AND m.channel_id=?`, p.CorrectionOf, channel).Scan(&prior); err != nil {
			return Outcome{}, errNotFound(err, "original input is unavailable")
		}
		conversation, err = communicationRoute(ctx, x, tx, p.CorrectionOf)
		if err != nil {
			return Outcome{}, err
		}
	}
	child := *x
	env := *x.Env
	child.Env = &env
	logicalReply := p.ReplyTo
	if logicalReply != "" {
		prior, err := store.MessageConversation(ctx, tx.Q(), logicalReply)
		if err != nil {
			return Outcome{}, err
		}
		if prior != conversation {
			logicalReply = ""
		}
	}
	env.Payload, _ = json.Marshal(protocol.SubmitMessagePayload{ConversationID: conversation, Body: p.Body, ReplyTo: logicalReply, Attachments: p.Attachments})
	origin := &communicationOrigin{Channel: channel, BotID: bot, ChatID: chat, ExternalID: external, ReplyTo: p.ReplyTo, CorrectionOf: p.CorrectionOf}
	out, err := (SubmitMessage{origin: origin, externalSource: externalEvidence}).Apply(ctx, &child, tx)
	if err != nil {
		return Outcome{}, err
	}

	if externalEvidence {
		id := out.Result.(map[string]any)["message_id"].(string)
		source := protocol.ManualSource{MessageID: id, ConversationID: conversation, SuppliedBy: x.Principal}
		raw, _ := json.Marshal(source)
		if _, err := tx.Exec(ctx, `INSERT INTO manual_message_sources(message_id,metadata_json) VALUES(?,?)`, id, string(raw)); err != nil {
			return Outcome{}, err
		}
	}

	return out, nil
}

func validatePublication(p protocol.MessagingPublication) error {
	if !utf8.ValidString(p.Body) || len(p.Body) > maxMessageBytes || (strings.TrimSpace(p.Body) == "" && len(p.Attachments) == 0) || len(p.Attachments) > 20 || len(p.SourceMessageIDs) > 100 || len(p.IssueIDs) > 50 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "nonempty bounded message or attachments required")
	}
	if p.Purpose != "progress" && p.Purpose != "final" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "purpose must be progress or final")
	}
	if p.ReplyTo == "" && (strings.TrimSpace(p.Reason) == "" || len(p.Reason) > 2000) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "notification requires a bounded business reason")
	}
	return nil
}

func publishCommunication(ctx context.Context, x *Exec, tx *store.Tx, p protocol.MessagingPublication, execution, conversation, author string) (Outcome, error) {
	if err := validatePublication(p); err != nil {
		return Outcome{}, err
	}
	if p.ReplyTo != "" {
		var err error
		conversation, err = store.MessageConversation(ctx, tx.Q(), p.ReplyTo)
		if err != nil {
			return Outcome{}, err
		}
	}
	id := x.IDs.New(protocol.PrefixMessage)
	seq, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: id, ConversationID: conversation, Author: author, Body: p.Body, ReplyTo: p.ReplyTo, RequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli()})
	if err != nil {
		return Outcome{}, err
	}
	if err = publishExistingCommunication(ctx, x, tx, id, p, execution); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: map[string]any{"message_id": id, "seq": seq}, Notify: true, Events: []Event{{ObjectType: "message", ObjectID: id, ObjectVersion: seq, Kind: "conversation.message", Body: map[string]any{"author": author}}}}, nil
}

func publishExistingCommunication(ctx context.Context, x *Exec, tx *store.Tx, id string, p protocol.MessagingPublication, execution string) error {
	if err := validatePublication(p); err != nil {
		return err
	}
	return registerCommunicationPublication(ctx, x, tx, id, p, execution)
}

// Internal notices already satisfy their business command's bounds. Transport
// splitting must not tighten an existing worker or owner request contract.
func registerCommunicationPublication(ctx context.Context, x *Exec, tx *store.Tx, id string, p protocol.MessagingPublication, execution string) error {
	channel := "web"
	var bot, chat, external int64
	direction := "notification"
	if p.ReplyTo != "" {
		direction = "publication"
		err := tx.Q().QueryRowContext(ctx, `SELECT channel_id,bot_id,chat_id,external_id FROM messaging_messages WHERE message_id=?`, p.ReplyTo).Scan(&channel, &bot, &chat, &external)
		if errors.Is(err, sql.ErrNoRows) {
			// A legacy input has no external endpoint. It can only receive a Web reply.
			var author string
			if err = tx.Q().QueryRowContext(ctx, `SELECT author FROM conversation_messages WHERE message_id=?`, p.ReplyTo).Scan(&author); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `INSERT INTO messaging_messages(message_id,direction,channel_id,reply_to,purpose,reason,execution_id) VALUES(?,?,?,?,?,?,?)`, id, direction, channel, p.ReplyTo, p.Purpose, p.Reason, execution)
	if err != nil {
		return err
	}
	sources := append([]string(nil), p.SourceMessageIDs...)
	if p.ReplyTo != "" {
		sources = append(sources, p.ReplyTo)
	}
	for _, source := range sources {
		if _, err := store.GetMessage(ctx, tx.Q(), source); err != nil {
			return errNotFound(err, "publication source unavailable")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO messaging_sources(message_id,source_message_id) VALUES(?,?) ON CONFLICT DO NOTHING`, id, source); err != nil {
			return err
		}
	}
	for _, issue := range p.IssueIDs {
		if _, err := store.GetCanonicalIssue(ctx, tx.Q(), issue); err != nil {
			return errNotFound(err, "publication Issue unavailable")
		}
		if err := bindCommunicationIssue(ctx, x, tx, id, issue); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for pos, file := range p.Attachments {
		if seen[file] {
			return protocol.Errorf(protocol.CodeInvalidRequest, "duplicate attachment")
		}
		seen[file] = true
		var conversation string
		if err = tx.Q().QueryRowContext(ctx, `SELECT conversation_id FROM conversation_messages WHERE message_id=?`, id).Scan(&conversation); err != nil {
			return err
		}
		if err = communicationAttachment(ctx, x, tx, file, conversation, execution); err != nil {
			return err
		}

		if err = tx.BindConversationAttachment(ctx, id, pos, file); err != nil {
			return err
		}
	}
	if p.ReplyTo != "" {
		if err = queueCommunication(ctx, x, tx, id, channel, bot, chat, external, p); err != nil {
			return err
		}
	} else {
		if err = queueCommunication(ctx, x, tx, id, "web", 0, 0, 0, p); err != nil {
			return err
		}
		var status string
		if err = tx.Q().QueryRowContext(ctx, `SELECT bot_id,chat_id,status FROM messaging_channels WHERE channel_id='telegram'`).Scan(&bot, &chat, &status); err != nil {
			return err
		}
		if status != "ready" {
			bot = 0
			chat = 0
		}
		if err = queueCommunication(ctx, x, tx, id, "telegram", bot, chat, 0, p); err != nil {
			return err
		}
	}
	return communicationEvent(ctx, x, tx, id, "messaging.published")
}

// MessagingPart is a frozen transport request. It never accepts agent addresses.
type MessagingPart struct {
	BindingRevision int64  `json:"binding_revision"`
	DeliveryID      string `json:"delivery_id"`
	MessageID       string `json:"message_id"`
	BotID           int64  `json:"bot_id"`
	ChatID          int64  `json:"chat_id"`
	ReplyExternalID int64  `json:"reply_external_id"`
	Body            string `json:"body,omitempty"`
	FileID          string `json:"file_id,omitempty"`
	Kind            string `json:"kind"`
}

func queueCommunication(ctx context.Context, x *Exec, tx *store.Tx, id, channel string, bot, chat, reply int64, p protocol.MessagingPublication) error {
	texts := splitCommunication(p.Body, 4000)
	parts := make([]MessagingPart, 0, len(texts)+len(p.Attachments))
	for _, body := range texts {
		parts = append(parts, MessagingPart{Body: body, Kind: "text"})
	}
	for _, file := range p.Attachments {
		parts = append(parts, MessagingPart{FileID: file, Kind: "file"})
	}
	for pos, part := range parts {
		if channel == "telegram" {
			if err := tx.Q().QueryRowContext(ctx, `SELECT revision FROM messaging_channels WHERE channel_id='telegram'`).Scan(&part.BindingRevision); err != nil {
				return err
			}
		}
		part.DeliveryID = x.IDs.New(protocol.PrefixOutbox)
		part.MessageID = id
		part.BotID = bot
		part.ChatID = chat
		part.ReplyExternalID = reply
		state := "queued"
		if channel == "web" {
			state = "accepted"
		}
		if channel == "telegram" && (bot == 0 || chat == 0) {
			state = "unavailable"
		}
		_, err := tx.Exec(ctx, `INSERT INTO messaging_deliveries(delivery_id,message_id,channel_id,part,kind,bot_id,chat_id,reply_external_id,body,file_id,state,created_at,updated_at,binding_revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, part.DeliveryID, id, channel, pos, part.Kind, bot, chat, reply, part.Body, part.FileID, state, x.Now.UnixMilli(), x.Now.UnixMilli(), part.BindingRevision)
		if err != nil {
			return err
		}
		if state != "queued" {
			continue
		}
		if err = insertMessagingOutbox(ctx, x, tx, part, MessagingDestination); err != nil {
			return err
		}
	}
	return nil
}

func splitCommunication(body string, limit int) []string {
	var result []string
	var chunk strings.Builder
	units := 0
	for _, r := range body {
		n := 1
		if r > 0xffff {
			n = 2
		}
		if units+n > limit {
			result = append(result, chunk.String())
			chunk.Reset()
			units = 0
		}
		chunk.WriteRune(r)
		units += n
	}
	if chunk.Len() > 0 {
		result = append(result, chunk.String())
	}
	return result
}

func recordCommunicationRead(ctx context.Context, x *Exec, tx *store.Tx, id string, revision int64, reader, endpoint, evidence, execution string) error {
	var current int64
	err := tx.Q().QueryRowContext(ctx, `SELECT d.content_revision FROM messaging_messages m JOIN message_dispositions d USING(message_id) WHERE m.message_id=?`, id).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) && reader == "agent" {
		return nil
	}
	if err != nil {
		return errNotFound(err, "communication message unavailable")
	}
	if revision == 0 {
		revision = current
		if execution != "" {
			err := tx.Q().QueryRowContext(ctx, `SELECT content_revision FROM messaging_native_inputs WHERE request_id=? AND message_id=?`, execution, id).Scan(&revision)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
	}
	if revision != current {
		return protocol.Errorf(protocol.CodeStaleRevision, "message content changed")
	}
	if reader == "agent" {
		conversation := store.ConversationID(x.Principal.ConversationID)
		var known string
		err := tx.Q().QueryRowContext(ctx, `SELECT m.conversation_id FROM main_turns t JOIN conversation_messages m ON m.message_id=t.message_id WHERE t.request_id=?`, execution).Scan(&known)
		if err == nil {
			conversation = known
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		reader = "agent:" + conversation
	}
	res, err := tx.Exec(ctx, `INSERT INTO messaging_reads(message_id,content_revision,reader,endpoint,evidence,execution_id,read_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, id, revision, reader, endpoint, evidence, execution, x.Now.UnixMilli())
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	if err = communicationEvent(ctx, x, tx, id, "messaging.read"); err != nil {
		return err
	}
	if strings.HasPrefix(reader, "agent:") {
		return queueCommunicationReads(ctx, x, tx)
	}
	return nil
}

// PublishCommunication is used through a fenced hosted coordination callback.
type PublishCommunication struct{ Notify bool }

func (c PublishCommunication) Operation() string {
	if c.Notify {
		return "conversation.notify"
	}
	return "conversation.publish"
}
func (PublishCommunication) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalMainAgent)
}
func (c PublishCommunication) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.MessagingPublication
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if c.Notify {
		p.ReplyTo = ""
	} else if p.ReplyTo == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "reply_to is required")
	}
	execution, err := x.reviewExecution(ctx, tx.Q())
	if err != nil {
		return Outcome{}, err
	}
	turn, err := store.GetMainTurn(ctx, tx.Q(), execution)
	if err != nil {
		return Outcome{}, err
	}
	if turn.ThreadID != x.Principal.Thread || turn.Status != store.TurnStarted {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "publication requires the active admitted execution")
	}
	conversation, err := store.MessageConversation(ctx, tx.Q(), turn.MessageID)
	if err != nil {
		return Outcome{}, err
	}
	if !c.Notify {
		if err = communicationReplyInput(ctx, tx.Q(), p.ReplyTo); err != nil {
			return Outcome{}, err
		}
		if err = communicationSourceAllowed(ctx, tx.Q(), execution, conversation, p.ReplyTo); err != nil {
			return Outcome{}, err
		}
	}
	if len(p.SourceMessageIDs) == 0 && c.Notify {
		p.SourceMessageIDs = []string{turn.MessageID}
	}
	for _, source := range p.SourceMessageIDs {
		if err = communicationSourceAllowed(ctx, tx.Q(), execution, conversation, source); err != nil {
			return Outcome{}, err
		}
	}
	if x.coordinationExecution != nil && x.coordinationExecution.IssueID != "" {
		for _, issue := range p.IssueIDs {
			if issue != x.coordinationExecution.IssueID {
				return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "publication is outside this Issue")
			}
		}
	}
	if x.coordinationExecution != nil && x.coordinationExecution.Selection.Purpose == agentconfig.Consultation && len(p.IssueIDs) != 0 {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "consultation notifications grant no Issue authority")
	}
	out, err := publishCommunication(ctx, x, tx, p, execution, conversation, "main_agent")
	if err != nil {
		return Outcome{}, err
	}
	id := out.Result.(map[string]any)["message_id"].(string)
	if _, err = tx.Exec(ctx, `UPDATE message_dispositions SET disposition=CASE WHEN disposition='pending' THEN 'unassigned' ELSE disposition END,classified_revision=content_revision,facts_revision=content_revision WHERE message_id=?`, id); err != nil {
		return Outcome{}, err
	}
	if x.coordinationExecution != nil && x.coordinationExecution.IssueID != "" {
		if err = bindCommunicationIssue(ctx, x, tx, id, x.coordinationExecution.IssueID); err != nil {
			return Outcome{}, err
		}
	}
	return out, nil
}

func communicationReplyInput(ctx context.Context, q store.Querier, id string) error {
	var direction string
	if err := q.QueryRowContext(ctx, `SELECT direction FROM messaging_messages WHERE message_id=?`, id).Scan(&direction); err != nil {
		return errNotFound(err, "reply input is unavailable")
	}
	if direction != "input" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "reply_to must identify an incoming communication; use conversation_notify for proactive output")
	}
	return nil
}

// Direct authenticated owner-panel answers are communication inputs too, but
// their existing decision event coordinates work instead of a second chat turn.
func registerWebCommunicationInput(ctx context.Context, tx *store.Tx, id, reply string) error {
	_, err := tx.Exec(ctx, `INSERT INTO messaging_messages(message_id,direction,channel_id,reply_to,source_revision) SELECT d.message_id,'input','web',?,d.content_revision FROM message_dispositions d WHERE d.message_id=?`, reply, id)
	return err
}

func communicationSourceAllowed(ctx context.Context, q store.Querier, execution, conversation, id string) error {
	if err := communicationSourceVisible(ctx, q, id); err != nil {
		return err
	}
	var admitted bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM main_turns t WHERE t.request_id=? AND t.message_id=?) OR EXISTS(SELECT 1 FROM logical_turn_executions e JOIN logical_turn_messages m USING(logical_turn_id) WHERE e.request_id=? AND m.message_id=?) OR EXISTS(SELECT 1 FROM main_turns t JOIN background_batches b ON b.message_id=t.message_id JOIN background_sources s USING(batch_id) WHERE t.request_id=? AND s.message_id=? AND s.cancelled_at=0)`, execution, id, execution, id, execution, id).Scan(&admitted)
	if err != nil {
		return err
	}
	if !admitted {
		return protocol.Errorf(protocol.CodeForbidden, "reply target is outside admitted source responsibility")
	}
	target, err := store.MessageConversation(ctx, q, id)
	if err != nil {
		return err
	}
	if target != conversation {
		var issue string
		err = q.QueryRowContext(ctx, `SELECT COALESCE(work_item_id,'') FROM agent_conversations WHERE conversation_id=?`, conversation).Scan(&issue)
		if err != nil {
			return err
		}
		if issue == "" {
			return protocol.Errorf(protocol.CodeForbidden, "reply target belongs to another conversation")
		}
		return hostedSourceInIssueScope(ctx, q, issue, id)
	}
	return nil
}

// An association or batch membership does not admit queued owner instructions.
func communicationSourceVisible(ctx context.Context, q store.Querier, id string) error {
	m, err := store.GetMessage(ctx, q, id)
	if err != nil {
		return err
	}
	if m.Author == "owner" {
		admitted, err := store.OwnerInputAdmitted(ctx, q, id)
		if err != nil {
			return err
		}
		var coordinator, withdrawn bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_return_links l JOIN main_turns t ON t.message_id=l.message_id WHERE l.source_message_id=? AND t.status IN ('starting','started','completed','interrupted','failed')),EXISTS(SELECT 1 FROM conversation_message_controls WHERE message_id=? AND withdrawn_at<>0)`, id, id).Scan(&coordinator, &withdrawn); err != nil {
			return err
		}
		if (!admitted && !coordinator) || withdrawn {
			return protocol.Errorf(protocol.CodeForbidden, "source is not admitted or has been withdrawn")
		}
	} else if m.Author == "system" {
		var cancelled bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM followup_occurrences WHERE message_id=? AND status='cancelled')`, id).Scan(&cancelled); err != nil {
			return err
		}
		if cancelled {
			return protocol.Errorf(protocol.CodeForbidden, "source was cancelled")
		}
	}
	return nil
}

// RecordMessagingConsumption persists witnessed native input consumption immediately.
func (a *App) RecordMessagingConsumption(ctx context.Context, execution, id, evidence string) error {
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		resolved, err := store.MainExecutionRequest(ctx, tx.Q(), execution)
		if err != nil {
			return err
		}
		execution = resolved
		turn, err := store.GetMainTurn(ctx, tx.Q(), execution)
		if err != nil {
			return err
		}
		conversation, err := store.MessageConversation(ctx, tx.Q(), turn.MessageID)
		if err != nil {
			return err
		}
		// A witnessed native item can arrive before StartTurn returns and its
		// started receipt commits. It proves consumption of this exact original
		// even if the owner withdrew it after sending; queued batch sources still
		// require their independent admission and withdrawal checks.
		if id != turn.MessageID {
			if err = communicationSourceAllowed(ctx, tx.Q(), execution, conversation, id); err != nil {
				return err
			}
		}
		x := &Exec{IDs: a.ids, Now: a.now(), Env: &protocol.Envelope{RequestID: execution}}
		return recordCommunicationRead(ctx, x, tx, id, 0, "agent", "agent", evidence, execution)
	})
	if err == nil {
		a.notifyChange()
		if a.notifier != nil {
			a.notifier.Notify()
		}
	}
	return err
}

func insertMessagingOutbox(ctx context.Context, x *Exec, tx *store.Tx, part MessagingPart, destination string) error {
	raw, err := json.Marshal(part)
	if err != nil {
		return err
	}
	return tx.InsertOutbox(ctx, store.OutboxRow{OutboxID: part.DeliveryID, RequestID: x.IDs.New(protocol.PrefixRequest), ParentRequestID: x.Env.RequestID, Destination: destination, Operation: "messaging.deliver", PayloadJSON: string(raw), CreatedAt: x.Now.UnixMilli()})
}

// registerSystemCommunication promotes only an explicitly user-facing program
// notice. Machine wake inputs remain internal unless their handler calls this.
func registerSystemCommunication(ctx context.Context, x *Exec, tx *store.Tx, id, reason string) error {
	var exists bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messaging_messages WHERE message_id=?)`, id).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	message, err := store.GetMessage(ctx, tx.Q(), id)
	if err != nil {
		return err
	}
	return registerCommunicationPublication(ctx, x, tx, id, protocol.MessagingPublication{Body: message.Body, Purpose: "final", Reason: reason}, "")
}
