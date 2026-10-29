package app

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type CreateConsultation struct{}

func (CreateConsultation) Operation() string { return "conversation.consult" }
func (CreateConsultation) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (CreateConsultation) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.CreateConsultationPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if x.hostedConfiguration == nil {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "named hosted configuration is unavailable")
	}
	prefix := x.hostedConfiguration.Public().ExecutionEnvironmentID + "/"
	if !strings.HasPrefix(p.AgentRef, prefix) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "select a registered hosted agent")
	}
	selected, err := x.hostedConfiguration.Select(strings.TrimPrefix(p.AgentRef, prefix), agentconfig.Consultation)
	if err != nil {
		return Outcome{}, err
	}
	if strings.TrimSpace(p.Title) == "" || len(p.Title) > 1000 || !utf8.ValidString(p.Title) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "a valid consultation title is required")
	}
	raw, err := json.Marshal(selected)
	if err != nil {
		return Outcome{}, err
	}
	id := "consultation:" + x.IDs.New(protocol.Prefix("conversation"))
	if _, err := tx.Exec(ctx, `INSERT INTO agent_conversations(conversation_id,model,effort,created_at) VALUES(?,'','',?)`, id, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO consultation_conversations(conversation_id,title,private_snapshot) VALUES(?,?,?)`, id, p.Title, string(raw)); err != nil {
		return Outcome{}, err
	}
	v, err := store.AgentConversation(ctx, tx.Q(), id)
	return Outcome{Result: v, Events: []Event{{ObjectType: "conversation", ObjectID: id, Kind: "conversation.consultation_created"}}}, err
}

type PromoteConsultation struct{}

func (PromoteConsultation) Operation() string { return "conversation.promote" }
func (PromoteConsultation) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (PromoteConsultation) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "conversation" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "target the consultation conversation")
	}
	c, err := store.AgentConversation(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	if c.Kind != "consultation" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "only consultation can be promoted")
	}
	var p protocol.PromoteConsultationPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	intent, err := store.GetMessage(ctx, tx.Q(), p.SourceMessageID)
	if err != nil {
		return Outcome{}, err
	}
	intentConversation, err := store.MessageConversation(ctx, tx.Q(), intent.MessageID)
	if err != nil {
		return Outcome{}, err
	}
	if intentConversation != c.ID || intent.Author != "owner" {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "execution intent must be an authenticated owner message in this consultation")
	}
	if err := validateCanonicalSourceReference(ctx, tx.Q(), canonicalOwnerIssueSource{SourceMessageID: p.SourceMessageID, ExpectedSourceRevision: p.ExpectedSourceRevision}); err != nil {
		return Outcome{}, err
	}
	if p.ExpectedSourceRevision == nil {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "select the actual source version")
	}
	if x.coordinationExecution != nil {
		var admitted bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM main_turns WHERE request_id=? AND message_id=?) OR EXISTS(SELECT 1 FROM main_steers s JOIN main_turns t ON t.thread_id=s.thread_id AND t.turn_id=s.turn_id WHERE t.request_id=? AND s.message_id=? AND s.native_item_id<>'')`, x.coordinationExecution.ID, p.SourceMessageID, x.coordinationExecution.ID, p.SourceMessageID).Scan(&admitted); err != nil {
			return Outcome{}, err
		}
		if !admitted {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "delegate only owner input admitted to this consultation execution")
		}
	}
	sources := append([]protocol.IssueSourceReference{p.IssueSourceReference}, p.Sources...)
	seen := map[string]bool{}
	for _, s := range sources {
		if seen[s.SourceMessageID] {
			continue
		}
		seen[s.SourceMessageID] = true
		m, err := store.GetMessage(ctx, tx.Q(), s.SourceMessageID)
		if err != nil {
			return Outcome{}, err
		}
		sourceConversation, err := store.MessageConversation(ctx, tx.Q(), m.MessageID)
		if err != nil {
			return Outcome{}, err
		}
		if sourceConversation != c.ID || s.ExpectedSourceRevision == nil {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "select persisted source versions from this consultation")
		}
		if err := validateCanonicalSourceReference(ctx, tx.Q(), canonicalOwnerIssueSource{SourceMessageID: s.SourceMessageID, ExpectedSourceRevision: s.ExpectedSourceRevision}); err != nil {
			return Outcome{}, err
		}
	}
	adapted := *x
	var out Outcome
	var issue protocol.Issue
	if p.ExistingIssueID == "" {
		if p.Assignee.Kind != protocol.IssueAssigneeAgent {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "delegate to a concrete registered Issue manager")
		}
		if err := resolveHostedAssignee(ctx, tx.Q(), x, p.Assignee); err != nil {
			return Outcome{}, err
		}
		payload := protocol.CreateIssuePayload{IssueSourceReference: p.IssueSourceReference, Plan: p.Plan, Assignee: &p.Assignee}
		raw, err := json.Marshal(payload)
		if err != nil {
			return Outcome{}, err
		}
		env := *x.Env
		env.Operation = "issues.create"
		env.Target = protocol.Target{Type: "issue"}
		env.ExpectedControlRevision = nil
		env.Payload = raw
		adapted.Env = &env
		out, err = createCanonicalOwnerIssueWithAssignment(ctx, &adapted, tx, true)
		if err != nil {
			return Outcome{}, err
		}
		var ok bool
		issue, ok = out.Result.(protocol.Issue)
		if !ok {
			return Outcome{}, protocol.Errorf(protocol.CodeInternal, "created Issue projection is unavailable")
		}
	} else {
		if !reflect.DeepEqual(p.Plan, protocol.IssuePlan{}) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "an existing Issue retains its definition; provide only the target and selected sources")
		}
		issue, err = store.GetCanonicalIssue(ctx, tx.Q(), p.ExistingIssueID)
		if err != nil {
			return Outcome{}, err
		}
		if p.ExpectedIssueRevision == nil || *p.ExpectedIssueRevision != issue.Revision || p.ExpectedAssignmentEpoch != issue.AssignmentEpoch {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "target Issue responsibility changed")
		}
		if p.Assignee != (protocol.IssueAssignee{}) && p.Assignee != issue.Assignee {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "an existing Issue retains its assignee")
		}
		if issue.State != protocol.IssueActive || issue.AssignmentState != "active" || issue.Assignee.Kind != protocol.IssueAssigneeAgent {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "target Issue needs an active registered manager")
		}
		if err := resolveHostedAssignee(ctx, tx.Q(), x, issue.Assignee); err != nil {
			return Outcome{}, err
		}
		out.Result = issue
	}
	if _, err := ensureCanonicalManagerConversation(ctx, tx, issue, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	for id := range seen {
		if err := tx.BindMessage(ctx, id, "work_item", issue.IssueID, "consultation_promotion"); err != nil {
			return Outcome{}, err
		}
		if err := appendBackgroundSource(ctx, x, tx, "work_item:"+issue.IssueID, issue.IssueID, id, "", "consultation.promoted", nil); err != nil {
			return Outcome{}, err
		}
	}
	out.Events = append(out.Events, Event{ObjectType: "conversation", ObjectID: c.ID, Kind: "conversation.promoted", Body: map[string]any{"issue_id": issue.IssueID, "source_message_id": p.SourceMessageID}})
	return out, nil
}
