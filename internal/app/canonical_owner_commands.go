package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// CanonicalOwnerIssueCommand changes definitions without registering a public
// operation or enabling canonical task, manager or lifecycle execution.
type CanonicalOwnerIssueCommand struct{ Action string }

func (c CanonicalOwnerIssueCommand) Operation() string { return "issues." + c.Action }
func (c CanonicalOwnerIssueCommand) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}

type canonicalOwnerIssueSource struct {
	SourceMessageID        string `json:"source_message_id,omitempty"`
	ExpectedSourceRevision *int64 `json:"expected_source_revision,omitempty"`
}
type canonicalOwnerIssueCreate struct {
	canonicalOwnerIssueSource
	Plan                   protocol.WorkItemPlan   `json:"plan"`
	Assignee               *protocol.IssueAssignee `json:"assignee,omitempty"`
	ParentIssueID          string                  `json:"parent_issue_id,omitempty"`
	ExpectedParentRevision *int64                  `json:"expected_parent_revision,omitempty"`
	DependsOn              []string                `json:"depends_on,omitempty"`
}
type canonicalOwnerIssueUpdate struct {
	canonicalOwnerIssueSource
	Plan                    protocol.WorkItemPlan `json:"plan"`
	ExpectedAssignmentEpoch int64                 `json:"expected_assignment_epoch"`
}

func (c CanonicalOwnerIssueCommand) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if c.Action != "create" && c.Action != "update" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unsupported canonical definition operation")
	}
	if x.Env.Target.Type != "issue" || x.Env.ParentRequestID != "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "canonical owner operation requires its own issue request")
	}
	if c.Action == "create" {
		if x.Env.Target.ID != "" || x.Env.ExpectedControlRevision != nil {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "new issue has no existing target or revision")
		}
		return createCanonicalOwnerIssue(ctx, x, tx)
	}
	if !protocol.ValidID(protocol.PrefixWorkItem, x.Env.Target.ID) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid canonical issue identity")
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	current, err := store.GetCanonicalIssue(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, errNotFound(err, "canonical issue does not exist")
	}
	if current.Revision != expected {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "issue revision changed")
	}
	return updateCanonicalOwnerIssue(ctx, x, tx, current)
}

func createCanonicalOwnerIssue(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p canonicalOwnerIssueCreate
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	owner := protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: x.Principal.Subject}
	if p.Assignee != nil && *p.Assignee != owner {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "only the authenticated owner is registered for assignment")
	}
	project, err := resolveProject(ctx, tx.Q(), p.Plan.Project, x.Principal)
	if err != nil {
		return Outcome{}, err
	}
	p.Plan.Project = project.Project
	if p.Plan.Stages == nil {
		p.Plan.Stages = []protocol.WorkItemStage{}
	}
	if err := validateCanonicalUnboundPlan(x, p.Plan, nil); err != nil {
		return Outcome{}, err
	}
	if err := validateCanonicalSourceReference(ctx, tx.Q(), p.canonicalOwnerIssueSource); err != nil {
		return Outcome{}, err
	}
	now := x.Now.UnixMilli()
	next := protocol.Issue{
		IssueControl: protocol.IssueControl{IssueID: x.IDs.New(protocol.PrefixWorkItem), ParentIssueID: p.ParentIssueID, State: protocol.IssueActive, Assignee: owner, AssignmentEpoch: 1, AssignmentState: "active"},
		Revision:     1, Plan: p.Plan, DependsOn: slices.Clone(p.DependsOn), CreatedAt: now, UpdatedAt: now,
	}
	if next.DependsOn == nil {
		next.DependsOn = []string{}
	}
	if err := validateCanonicalOwnerGraph(ctx, tx.Q(), next, p.ExpectedParentRevision, true); err != nil {
		return Outcome{}, err
	}
	return saveCanonicalOwnerDefinition(ctx, x, tx, next, 0, p.canonicalOwnerIssueSource, p, "issue.created")
}

func updateCanonicalOwnerIssue(ctx context.Context, x *Exec, tx *store.Tx, current protocol.Issue) (Outcome, error) {
	var p canonicalOwnerIssueUpdate
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if err := domain.CheckIssueEpoch(current.IssueControl, p.ExpectedAssignmentEpoch); err != nil {
		return Outcome{}, err
	}
	if current.State != protocol.IssueActive && current.State != protocol.IssuePaused {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "reopen the issue before revising its definition")
	}
	if p.Plan.Project != current.Plan.Project {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "project transfer requires an explicit authority change")
	}
	if p.Plan.Stages == nil {
		p.Plan.Stages = []protocol.WorkItemStage{}
	}
	if err := validateCanonicalUnboundPlan(x, p.Plan, &current.Plan); err != nil {
		return Outcome{}, err
	}
	if err := validateCanonicalSourceReference(ctx, tx.Q(), p.canonicalOwnerIssueSource); err != nil {
		return Outcome{}, err
	}
	// A lifecycle-safe future update must inspect immutable primary task agreements.
	// b2 keeps canonical task creation disabled; still refuse an unexpected persisted
	// primary rather than silently treating empty stages as an empty responsibility.
	var primary bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_work_item_owners WHERE work_item_id=?)`, current.IssueID).Scan(&primary); err != nil {
		return Outcome{}, err
	}
	if primary {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "canonical task agreement updates are not enabled")
	}
	if reflect.DeepEqual(current.Plan, p.Plan) && p.SourceMessageID == "" {
		// Canonical controls retain an exact success receipt even without a business
		// change. Replays reauthorize and return this result after later revisions.
		return Outcome{Result: current}, nil
	}
	next := current
	next.Plan = p.Plan
	next.Revision++
	next.UpdatedAt = x.Now.UnixMilli()
	// Definition edits keep graph integrity without pretending that unchanged
	// child membership is a new activation under a cancelling ancestor.
	if err := validateCanonicalOwnerGraph(ctx, tx.Q(), next, nil, false); err != nil {
		return Outcome{}, err
	}
	return saveCanonicalOwnerDefinition(ctx, x, tx, next, current.Revision, p.canonicalOwnerIssueSource, p, "issue.updated")
}

func validateCanonicalUnboundPlan(x *Exec, plan protocol.WorkItemPlan, previous *protocol.WorkItemPlan) error {
	if plan.DefaultWorkspaceTaskID != "" {
		return protocol.Errorf(protocol.CodeInvalidTransition, "canonical task binding is not enabled")
	}
	for _, stage := range plan.Stages {
		if len(stage.TaskIDs) > 0 {
			return protocol.Errorf(protocol.CodeInvalidTransition, "canonical task binding is not enabled")
		}
	}
	// This minimal definition slice supports authentic current-owner evidence and
	// unchanged server-frozen prior evidence only. Additional proof kinds need the
	// lifecycle/observation reader before being enabled, not a trusted JSON blob.
	var prior *protocol.WorkItem
	if previous != nil {
		prior = &protocol.WorkItem{WorkItemPlan: *previous}
	}
	validate := func(e protocol.WorkItemEvidence) error {
		if saved, ok := savedIssueEvidence(prior, e); ok {
			if !reflect.DeepEqual(saved, e) {
				return protocol.Errorf(protocol.CodeInvalidRequest, "recorded evidence snapshots are immutable")
			}
			return nil
		}
		if e.Kind != protocol.WorkItemEvidenceOwnerDecision || e.Reference != "current_request" || x.Principal.Kind != protocol.PrincipalOwner || e.Observation != nil || e.Upgrade != nil {
			return protocol.Errorf(protocol.CodeInvalidRequest, "new definition evidence requires this exact owner request")
		}
		return nil
	}
	for i, stage := range plan.Stages {
		if len(stage.Evidence) > 0 {
			if err := domain.ValidateIssueEvidenceAt(stage.Evidence, fmt.Sprintf("stages[%d].evidence", i)); err != nil {
				return err
			}
		}
		for _, e := range stage.Evidence {
			if err := validate(e); err != nil {
				return err
			}
		}
	}
	for i, follow := range plan.FollowOns {
		if len(follow.Evidence) > 0 {
			if err := domain.ValidateIssueEvidenceAt(follow.Evidence, fmt.Sprintf("follow_ons[%d].evidence", i)); err != nil {
				return err
			}
		}
		for _, e := range follow.Evidence {
			if err := validate(e); err != nil {
				return err
			}
		}
	}
	return domain.ValidateIssue(plan, previous)
}

func saveCanonicalOwnerDefinition(ctx context.Context, x *Exec, tx *store.Tx, next protocol.Issue, expected int64, source canonicalOwnerIssueSource, reason any, kind string) (Outcome, error) {
	projection := protocol.WorkItem{WorkItemPlan: next.Plan}
	freezeIssueCurrentEvidence(&projection, x.Env.RequestID)
	next.Plan = projection.WorkItemPlan
	// The store freezes dependencies in sorted order; the response and durable
	// request receipt must describe that same aggregate, regardless of input order.
	next.DependsOn = slices.Clone(next.DependsOn)
	slices.Sort(next.DependsOn)
	if err := tx.SaveCanonicalIssue(ctx, next, expected, store.IssueRevisionProvenance{Actor: x.Principal, RequestID: x.Env.RequestID, SourceMessageID: source.SourceMessageID, Reason: reason}); err != nil {
		return Outcome{}, err
	}
	// No BindMessage(work_item), native conversation or legacy background notice:
	// the source is retained in the immutable revision and current owner request.
	// P1b3/P4 add exact canonical sourced context and coordination effects.
	return Outcome{Result: next, Events: []Event{{ObjectType: "issue", ObjectID: next.IssueID, ObjectVersion: next.Revision, Kind: kind, Body: map[string]any{"revision": next.Revision, "state": next.State}}}}, nil
}

// validateCanonicalSourceReference reads material explicitly cited by an already
// authorized owner command. It neither admits an agent source nor transfers the
// message's responsibility or author identity.
func validateCanonicalSourceReference(ctx context.Context, q store.Querier, source canonicalOwnerIssueSource) error {
	if source.SourceMessageID == "" {
		if source.ExpectedSourceRevision != nil {
			return protocol.Errorf(protocol.CodeInvalidRequest, "source revision requires a message")
		}
		return nil
	}
	if !protocol.ValidID(protocol.PrefixMessage, source.SourceMessageID) || source.ExpectedSourceRevision == nil || *source.ExpectedSourceRevision < 1 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "source requires exact message and content revision")
	}
	var revision int64
	var withdrawn bool
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(d.content_revision,1),COALESCE(c.withdrawn_at,0)<>0
 FROM conversation_messages m LEFT JOIN message_dispositions d USING(message_id)
 LEFT JOIN conversation_message_controls c USING(message_id)
 WHERE m.message_id=? AND m.kind='message'`, source.SourceMessageID).Scan(&revision, &withdrawn); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return protocol.Errorf(protocol.CodeInvalidRequest, "source message does not exist")
		}
		return fmt.Errorf("app: read source reference: %w", err)
	}
	if revision != *source.ExpectedSourceRevision {
		return protocol.Errorf(protocol.CodeStaleRevision, "source content changed")
	}
	if withdrawn {
		return protocol.Errorf(protocol.CodeInvalidTransition, "source was withdrawn")
	}
	return nil
}

func validateCanonicalOwnerGraph(ctx context.Context, q store.Querier, next protocol.Issue, expectedParent *int64, activation bool) error {
	if next.ParentIssueID == "" && expectedParent != nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "parent revision requires a parent")
	}
	rows, err := q.QueryContext(ctx, `SELECT c.issue_id FROM issue_controls c JOIN work_items w ON w.work_item_id=c.issue_id WHERE w.project=? ORDER BY c.issue_id`, next.Plan.Project)
	if err != nil {
		return err
	}
	var ids []string
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
	controls := map[string]protocol.IssueControl{}
	var edges []protocol.IssueDependency
	parentFound := next.ParentIssueID == ""
	for _, id := range ids {
		issue, err := store.GetCanonicalIssue(ctx, q, id)
		if err != nil {
			return err
		}
		if id == next.ParentIssueID {
			parentFound = true
			if expectedParent != nil && *expectedParent != issue.Revision {
				return protocol.Errorf(protocol.CodeStaleRevision, "parent issue changed")
			}
		}
		if id == next.IssueID {
			continue
		}
		controls[id] = issue.IssueControl
		for _, dep := range issue.DependsOn {
			edges = append(edges, protocol.IssueDependency{IssueID: id, DependsOn: dep})
		}
	}
	if !parentFound {
		return protocol.Errorf(protocol.CodeInvalidRequest, "canonical parent is missing or outside project")
	}
	controls[next.IssueID] = next.IssueControl
	for _, dep := range next.DependsOn {
		edges = append(edges, protocol.IssueDependency{IssueID: next.IssueID, DependsOn: dep})
	}
	nodes := make([]protocol.IssueControl, 0, len(controls))
	for _, control := range controls {
		nodes = append(nodes, control)
	}
	slices.SortFunc(nodes, func(a, b protocol.IssueControl) int { return strings.Compare(a.IssueID, b.IssueID) })
	if err := domain.ValidateIssueGraph(nodes, edges); err != nil {
		return err
	}
	if activation {
		return domain.ValidateIssueAncestorActivation(next.ParentIssueID, controls)
	}
	return nil
}
