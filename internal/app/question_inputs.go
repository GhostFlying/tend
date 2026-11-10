package app

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// QuestionInputCommand adds or revokes exact original-run attachment authority.
type QuestionInputCommand struct{ Revoke bool }

func (c QuestionInputCommand) Operation() string {
	if c.Revoke {
		return "decisions.revoke_inputs"
	}
	return "decisions.grant_inputs"
}
func (QuestionInputCommand) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner, protocol.PrincipalMainAgent)
}
func (c QuestionInputCommand) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "decision" || x.Env.ParentRequestID != "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "independent decision request required")
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	var p protocol.QuestionInputPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if strings.TrimSpace(p.Reason) == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "explicit input reason required")
	}
	d, err := store.GetDecision(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, errNotFound(err, "worker question does not exist")
	}
	if d.Worker == nil {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "only a frozen worker question accepts input grants")
	}
	page, err := store.QuestionInputs(ctx, tx.Q(), d.DecisionID)
	if err != nil {
		return Outcome{}, err
	}
	if err := questionInputScope(ctx, tx.Q(), d, &page); err != nil {
		return Outcome{}, err
	}
	if page.Revision != expected {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "question input revision changed")
	}
	if x.Principal.Kind != protocol.PrincipalOwner {
		if x.coordinationExecution == nil || x.coordinationExecution.Selection.Purpose != agentconfig.Main || p.SourceMessageID == "" {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "only an admitted main execution can relay owner input")
		}
		if err := validateAttentionOwnerRelay(ctx, tx.Q(), x, "", p.SourceMessageID); err != nil {
			return Outcome{}, err
		}
	}
	if err := validateCanonicalSourceReference(ctx, tx.Q(), canonicalOwnerIssueSource(p.IssueSourceReference)); err != nil {
		return Outcome{}, err
	}
	revision := page.Revision + 1
	if c.Revoke {
		if len(p.FileIDs) != 0 || p.GrantRevision < 1 {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "revoke requires only an exact grant_revision")
		}
		found := false
		for _, g := range page.Grants {
			if g.Revision == p.GrantRevision && g.Status == "active" {
				found = true
			}
		}
		if !found {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "active grant does not exist")
		}
		for i := range page.Grants {
			if page.Grants[i].Revision == p.GrantRevision {
				page.Grants[i].Status = "revoked"
			}
		}
		page.Revision = revision
		return Outcome{Result: page, Events: []Event{{ObjectType: "question_inputs", ObjectID: d.DecisionID, ObjectVersion: revision, Kind: "question.inputs_revoked", Body: map[string]any{"grant_revision": p.GrantRevision, "reason": p.Reason, "source": p.IssueSourceReference}}}}, nil
	}
	if p.GrantRevision != 0 || len(p.FileIDs) == 0 || len(p.FileIDs) > 20 || len(dedupe(p.FileIDs)) != len(p.FileIDs) || p.Version < 1 || p.SourceMessageID == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "current question version, owner source and distinct file_ids required")
	}
	if p.Version != d.Version {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "question version changed")
	}
	if d.Status != store.DecisionOpen && d.Status != store.DecisionAnswered {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "question no longer applies")
	}
	source, err := store.GetMessage(ctx, tx.Q(), p.SourceMessageID)
	if err != nil {
		return Outcome{}, errNotFound(err, "owner source does not exist")
	}
	ownerAuthority, err := store.HasOwnerMessageAuthority(ctx, tx.Q(), p.SourceMessageID)
	if err != nil {
		return Outcome{}, err
	}
	if !ownerAuthority || (source.MessageID != d.AnsweredMessageID && source.ReplyTo != d.AskedMessageID) {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "source must be the actual owner answer or reply to the original question")
	}
	rc, err := loadRun(ctx, tx.Q(), d.Worker.RunID)
	if err != nil {
		return Outcome{}, err
	}
	lease, err := store.GetLeaseByRun(ctx, tx.Q(), rc.run.RunID)
	if err != nil {
		return Outcome{}, err
	}
	if rc.run.RunID != p.RunID || lease.LeaseID != p.LeaseID || lease.Status == store.LeaseReleased {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "exact original run and lease required")
	}
	if rc.run.Role != protocol.RoleImplement && rc.run.Role != protocol.RoleAccept {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "only original implementation or acceptance worker inputs are supported")
	}
	if err := questionInputRunCurrent(ctx, tx.Q(), rc); err != nil {
		return Outcome{}, err
	}
	execution, err := store.GetRunExecution(ctx, tx.Q(), p.RunID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Outcome{}, err
	}
	files, err := store.ConversationAttachments(ctx, tx.Q(), p.SourceMessageID)
	if err != nil {
		return Outcome{}, err
	}
	g := protocol.QuestionInputGrant{Format: 1, Revision: revision, Status: "active", DecisionID: d.DecisionID, TaskID: rc.task.TaskID, RunID: p.RunID, LeaseID: p.LeaseID, ActionID: rc.action.ActionID, Role: rc.run.Role, SpecVersion: d.Worker.SpecVersion, Generation: rc.action.Generation, Execution: execution, SourceMessageID: p.SourceMessageID, SourceRevision: *p.ExpectedSourceRevision, Reason: p.Reason}
	for _, id := range p.FileIDs {
		found := false
		for _, f := range files {
			if f.FileID == id {
				g.Files = append(g.Files, protocol.TaskInput{FileID: f.FileID, Name: f.Name, MediaType: f.MediaType, Size: f.Size, SHA256: f.SHA256, SourceMessageID: p.SourceMessageID})
				found = true
				break
			}
		}
		if !found {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "file is not attached to the exact owner reply")
		}
	}
	page.Revision = revision
	page.Grants = append(page.Grants, g)
	return Outcome{Result: page, Events: []Event{{ObjectType: "question_inputs", ObjectID: d.DecisionID, ObjectVersion: revision, Kind: "question.inputs_granted", Body: map[string]any{"grant": g}}}}, nil
}

func questionInputRunCurrent(ctx context.Context, q store.Querier, rc runContext) error {
	if desiredFor(rc.run, rc.action, rc.task) == protocol.DesiredStopped || rc.task.Lifecycle.Terminal() || rc.action.TargetSpecVersion != rc.task.SpecVersion || rc.action.Status == protocol.ActionCancelled || rc.action.Status == protocol.ActionSuperseded || (rc.action.Kind == protocol.ActionImplement && rc.action.Generation != rc.task.ImplementGeneration) || (rc.action.Kind == protocol.ActionAccept && rc.action.TargetArtifactID != rc.task.CurrentArtifactID) {
		return protocol.Errorf(protocol.CodeStaleRevision, "original question execution no longer applies")
	}
	owner, err := store.TaskOwnerContractCurrent(ctx, q, rc.task.TaskID)
	if err != nil {
		return err
	}
	current, err := store.RunIssueResponsibilityCurrent(ctx, q, rc.run.RunID)
	if err != nil {
		return err
	}
	if !owner || !current {
		return protocol.Errorf(protocol.CodeStaleRevision, "original responsibility changed")
	}
	return nil
}

// QuestionInputs is owner diagnostics; workers use their existing question read.
func (a *App) QuestionInputs(ctx context.Context, decision string) (protocol.QuestionInputPage, error) {
	var out protocol.QuestionInputPage
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		d, err := store.GetDecision(ctx, tx.Q(), decision)
		if err != nil {
			return errNotFound(err, "question does not exist")
		}
		if d.Worker == nil {
			return protocol.Errorf(protocol.CodeInvalidRequest, "not a worker question")
		}
		out, err = store.QuestionInputs(ctx, tx.Q(), decision)
		if err != nil {
			return err
		}
		return questionInputScope(ctx, tx.Q(), d, &out)
	})
	return out, err
}

func questionInputFile(ctx context.Context, q store.Querier, rc runContext, id string) (*protocol.TaskInput, error) {
	if err := questionInputRunCurrent(ctx, q, rc); err != nil {
		if protocol.CodeOf(err) == protocol.CodeStaleRevision {
			return nil, nil
		}
		return nil, err
	}
	lease, err := store.GetLeaseByRun(ctx, q, rc.run.RunID)
	if err != nil {
		return nil, err
	}
	questions, err := store.ListWorkerQuestions(ctx, q, rc.run.RunID)
	if err != nil {
		return nil, err
	}
	for _, question := range questions {
		if question.Status != store.DecisionOpen && question.Status != store.DecisionAnswered {
			continue
		}
		page, err := store.QuestionInputs(ctx, q, question.DecisionID)
		if err != nil {
			return nil, err
		}
		for _, g := range page.Grants {
			if lease.Status == store.LeaseReleased || g.Status != "active" || g.RunID != rc.run.RunID || g.LeaseID != lease.LeaseID || g.Role != rc.run.Role || g.TaskID != rc.task.TaskID || g.ActionID != rc.action.ActionID || g.SpecVersion != rc.action.TargetSpecVersion || g.Generation != rc.action.Generation {
				continue
			}
			pin, err := store.GetRunExecution(ctx, q, rc.run.RunID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
			if !reflect.DeepEqual(pin, g.Execution) {
				continue
			}
			for _, f := range g.Files {
				if f.FileID == id {
					return &f, nil
				}
			}
		}
	}
	return nil, nil
}

func questionInputScope(ctx context.Context, q store.Querier, d store.DecisionRow, out *protocol.QuestionInputPage) error {
	w, err := store.GetWorkerQuestionRow(ctx, q, d.DecisionID)
	if err != nil {
		return err
	}
	r, err := store.GetRun(ctx, q, w.RunID)
	if err != nil {
		return err
	}
	a, err := store.GetAction(ctx, q, w.ActionID)
	if err != nil {
		return err
	}
	l, err := store.GetLeaseByRun(ctx, q, w.RunID)
	if err != nil {
		return err
	}
	out.QuestionVersion = d.Version
	out.Original = &protocol.QuestionInputScope{TaskID: r.TaskID, RunID: r.RunID, LeaseID: l.LeaseID, ActionID: w.ActionID, SpecVersion: w.SpecVersion, Role: r.Role, Generation: a.Generation}
	return nil
}
