package app

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type canonicalOwnerControl struct {
	canonicalOwnerIssueSource
	ExpectedAssignmentEpoch int64  `json:"expected_assignment_epoch"`
	Reason                  string `json:"reason,omitempty"`
}
type canonicalOwnerFollowOnDisposition struct {
	FollowOnID string                   `json:"follow_on_id"`
	Reason     string                   `json:"reason"`
	Evidence   []protocol.IssueEvidence `json:"evidence"`
}
type canonicalOwnerSourceDisposition struct {
	MessageID           string `json:"message_id"`
	ContentRevision     int64  `json:"content_revision"`
	AssociationRevision int64  `json:"association_revision"`
	Reason              string `json:"reason"`
}
type canonicalOwnerEndpoint struct {
	canonicalOwnerControl
	Closure                 *protocol.IssueClosure                     `json:"closure,omitempty"`
	FollowOnDispositions    []canonicalOwnerFollowOnDisposition        `json:"follow_on_dispositions,omitempty"`
	SourceDispositions      []canonicalOwnerSourceDisposition          `json:"source_dispositions,omitempty"`
	ExpectedContextRevision *int64                                     `json:"expected_context_revision,omitempty"`
	CompletionReviews       []protocol.CompletionReviewAcknowledgement `json:"completion_reviews,omitempty"`
}

// applyCanonicalOwnerLifecycle runs after the existing command has authenticated
// owner, checked target/revision and loaded the aggregate. Task execution uses
// the separate owner task command; manager execution remains unavailable.
func applyCanonicalOwnerLifecycle(ctx context.Context, x *Exec, tx *store.Tx, action string, current protocol.Issue) (Outcome, error) {
	var common canonicalOwnerControl
	var reason any
	next := current
	var endpoint *canonicalOwnerEndpoint
	var expectedParent *int64
	activation := false
	var completionReviews []protocol.CompletionReviewAcknowledgement
	var taskEvents []Event
	switch action {
	case "pause", "resume", "cancel", "reopen":
		if err := decodePayload(x.Env, &common); err != nil {
			return Outcome{}, err
		}
		reason = common
	case "close", "finish_cancel":
		var p canonicalOwnerEndpoint
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		common = p.canonicalOwnerControl
		endpoint = &p
		reason = p
	case "review_completions":
		var p struct {
			canonicalOwnerControl
			Reviews []protocol.CompletionReviewAcknowledgement `json:"reviews"`
		}
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		common = p.canonicalOwnerControl
		completionReviews = p.Reviews
		reason = p
		if len(completionReviews) == 0 {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "explicit pending completion reviews are required")
		}
	case "review_sources":
		var p struct {
			canonicalOwnerControl
			SourceDispositions      []canonicalOwnerSourceDisposition `json:"source_dispositions"`
			ExpectedContextRevision *int64                            `json:"expected_context_revision"`
		}
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		common = p.canonicalOwnerControl
		endpoint = &canonicalOwnerEndpoint{canonicalOwnerControl: common, SourceDispositions: p.SourceDispositions, ExpectedContextRevision: p.ExpectedContextRevision}
		reason = p
	case "reparent":
		var p struct {
			canonicalOwnerControl
			ParentIssueID          string `json:"parent_issue_id"`
			ExpectedParentRevision *int64 `json:"expected_parent_revision,omitempty"`
		}
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		common = p.canonicalOwnerControl
		reason = p
		next.ParentIssueID = p.ParentIssueID
		expectedParent = p.ExpectedParentRevision
		activation = true
	case "dependencies":
		var p struct {
			canonicalOwnerControl
			DependsOn *[]string `json:"depends_on"`
		}
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		common = p.canonicalOwnerControl
		reason = p
		if p.DependsOn == nil {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "explicit dependency replacement is required")
		}
		next.DependsOn = slices.Clone(*p.DependsOn)
		if next.DependsOn == nil {
			next.DependsOn = []string{}
		}
		slices.Sort(next.DependsOn)
	case "assign":
		var p struct {
			canonicalOwnerControl
			Assignee protocol.IssueAssignee `json:"assignee"`
		}
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		common = p.canonicalOwnerControl
		reason = p
		if p.Assignee != (protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: x.Principal.Subject}) || p.Assignee != current.Assignee {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "only the current registered owner is available")
		}
	default:
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unsupported canonical control action")
	}
	if err := domain.CheckIssueEpoch(current.IssueControl, common.ExpectedAssignmentEpoch); err != nil {
		return Outcome{}, err
	}
	if err := validateCanonicalSourceReference(ctx, tx.Q(), common.canonicalOwnerIssueSource); err != nil {
		return Outcome{}, err
	}

	var err error
	switch action {
	case "pause":
		next.IssueControl, err = domain.PauseIssue(current.IssueControl)
	case "resume":
		next.IssueControl, err = domain.ResumeIssue(current.IssueControl)
	case "cancel":
		next.IssueControl, err = domain.BeginIssueCancellation(current.IssueControl)
		if err == nil {
			taskEvents, err = cancelCanonicalPrimaryTasks(ctx, x, tx, current.IssueID)
		}
	case "reopen":
		next.IssueControl, err = domain.ReopenIssueControl(current.IssueControl)
		next.Closure = nil
		activation = true
	case "reparent", "dependencies", "assign":
		if current.State != protocol.IssueActive && current.State != protocol.IssuePaused {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "reopen responsibility before changing its structure")
		}
		if action == "reparent" {
			f, e := canonicalOwnerEndpointFacts(ctx, tx.Q(), current)
			if e != nil {
				return Outcome{}, e
			}
			managers, e := hostedReparentBlockers(ctx, tx.Q(), current.IssueID)
			if e != nil {
				return Outcome{}, e
			}
			f.ManagerTurns = append(f.ManagerTurns, managers...)
			err = domain.ValidateIssueReparentFacts(f)
		}
	case "review_completions":
		err = acknowledgeCanonicalCompletions(ctx, tx, current.IssueID, current.Revision+1, completionReviews)
	case "review_sources":
		if current.State != protocol.IssueClosed && current.State != protocol.IssueCancelled {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "source-only settlement requires a terminal issue")
		}
		if len(endpoint.SourceDispositions) == 0 {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "explicit source dispositions are required")
		}
		if err = applyCanonicalSourceDispositions(ctx, tx, current.IssueID, *endpoint); err != nil {
			return Outcome{}, err
		}

		// The owner explicitly maintains the current endpoint. No ancestor is
		// activated, and original plan, closure and assignment remain unchanged.
	case "close", "finish_cancel":
		if action == "close" && endpoint.Closure == nil {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "explicit closure is required")
		}
		if action == "finish_cancel" && (endpoint.Closure != nil || strings.TrimSpace(common.Reason) == "") {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "cancellation requires an explicit reason, not a completion closure")
		}
		if err = applyCanonicalFollowOnDispositions(&next.Plan, endpoint.FollowOnDispositions, func(evidence []protocol.IssueEvidence) error {
			return canonicalEndpointEvidence(ctx, x, tx.Q(), next, evidence)
		}); err != nil {
			return Outcome{}, err
		}
		if err = validateCanonicalBoundPlan(ctx, x, tx.Q(), current.IssueID, next.Plan, &current.Plan); err != nil {
			return Outcome{}, err
		}
		if err = applyCanonicalSourceDispositions(ctx, tx, current.IssueID, *endpoint); err != nil {
			return Outcome{}, err
		}
		if err = acknowledgeCanonicalCompletions(ctx, tx, current.IssueID, current.Revision+1, endpoint.CompletionReviews); err != nil {
			return Outcome{}, err
		}
		f, e := canonicalOwnerEndpointFacts(ctx, tx.Q(), next)
		if e != nil {
			return Outcome{}, e
		}
		if blockers := domain.IssueEndpointBlockers(f); len(blockers) > 0 {
			return Outcome{}, canonicalEndpointError("issue endpoint has unsettled obligations", blockers)
		}
		if action == "close" {
			next.Closure = endpoint.Closure
			// This slice accepts the explicit current owner's endpoint, not a caller
			// supplied acceptance/observation snapshot or a request/event proof chain.
			if err = canonicalEndpointEvidence(ctx, x, tx.Q(), next, next.Closure.Evidence); err != nil {
				return Outcome{}, err
			}
			projection := protocol.WorkItem{IssuePlan: next.Plan, Closure: next.Closure}
			validateClosure := domain.ValidateIssueClosure
			if x.coordinationExecution != nil {
				validateClosure = domain.ValidateResponsibleIssueClosure
			}
			if err = validateClosure(projection, *next.Closure); err != nil {
				return Outcome{}, err
			}
			if next.Closure.Kind == protocol.WorkItemClosureCompleted {
				if err = checkCanonicalCompletionPrerequisites(ctx, tx.Q(), next); err != nil {
					return Outcome{}, err
				}
			}
			next.IssueControl, err = domain.CloseIssueControl(current.IssueControl, f)
		} else {
			next.IssueControl, err = domain.FinishIssueCancellation(current.IssueControl, f)
			next.Closure = nil
		}
	}
	if err != nil {
		return Outcome{}, err
	}
	if err = validateCanonicalOwnerGraph(ctx, tx.Q(), next, expectedParent, activation); err != nil {
		return Outcome{}, err
	}
	if action != "review_sources" && action != "review_completions" && reflect.DeepEqual(current, next) && common.SourceMessageID == "" {
		return Outcome{Result: current}, nil
	}
	next.Revision++
	next.UpdatedAt = x.Now.UnixMilli()
	projection := protocol.WorkItem{IssuePlan: next.Plan, Closure: next.Closure}
	freezeIssueCurrentEvidence(&projection, x.Env.RequestID)
	next.Plan, next.Closure = projection.IssuePlan, projection.Closure
	out, err := saveCanonicalOwnerDefinition(ctx, x, tx, next, current.Revision, common.canonicalOwnerIssueSource, reason, canonicalLifecycleEvent(action))
	if err != nil {
		return Outcome{}, err
	}
	out.Events = append(out.Events, taskEvents...)
	// Canonical save increments context revision through 0043. Review the ended C
	// share at that final revision; do not mark a shared logical/native turn done.
	if endpoint != nil {
		if err = recordCanonicalEndpointContextReviews(ctx, tx, next.IssueID); err != nil {
			return Outcome{}, err
		}
	}
	return out, nil
}

func canonicalLifecycleEvent(action string) string {
	if action == "review_completions" {
		return "issue.completions_reviewed"
	}

	return map[string]string{"pause": "issue.paused", "resume": "issue.resumed", "cancel": "issue.cancelling", "finish_cancel": "issue.cancelled", "close": "issue.closed", "reopen": "issue.reopened", "reparent": "issue.reparented", "dependencies": "issue.dependencies_changed", "assign": "issue.assignment_confirmed", "review_sources": "issue.sources_reviewed"}[action]
}

func applyCanonicalFollowOnDispositions(plan *protocol.IssuePlan, dispositions []canonicalOwnerFollowOnDisposition, validateEvidence func([]protocol.IssueEvidence) error) error {
	next := *plan
	next.FollowOns = slices.Clone(plan.FollowOns)
	indexes := map[string]int{}
	for i, f := range plan.FollowOns {
		indexes[f.FollowOnID] = i
	}
	seen := map[string]bool{}
	for _, d := range dispositions {
		i, ok := indexes[d.FollowOnID]
		if !ok || seen[d.FollowOnID] || strings.TrimSpace(d.Reason) == "" {
			return protocol.Errorf(protocol.CodeInvalidRequest, "distinct existing follow-on and reason are required")
		}
		seen[d.FollowOnID] = true
		if next.FollowOns[i].Status != protocol.WorkItemFollowOnOpen {
			return protocol.Errorf(protocol.CodeInvalidTransition, "only open follow-ons admit disposition")
		}
		if err := validateEvidence(d.Evidence); err != nil {
			return err
		}
		next.FollowOns[i].Status = protocol.WorkItemFollowOnResolved
		next.FollowOns[i].Evidence = append(slices.Clone(plan.FollowOns[i].Evidence), d.Evidence...)
	}
	if err := domain.ValidateIssue(next, plan); err != nil {
		return err
	}
	*plan = next
	return nil
}
func validateCanonicalOwnerEndpointEvidence(evidence []protocol.IssueEvidence) error {
	if err := domain.ValidateIssueEvidence(evidence); err != nil {
		return err
	}
	for _, e := range evidence {
		if e.Kind != protocol.WorkItemEvidenceOwnerDecision || e.Reference != "current_request" || e.Observation != nil || e.Upgrade != nil {
			return protocol.Errorf(protocol.CodeInvalidRequest, "endpoint evidence requires this owner decision")
		}
	}
	return nil
}

func canonicalEndpointError(message string, blockers []protocol.IssueBlocker) error {
	details, _ := json.Marshal(map[string]any{"blockers": blockers})
	return &protocol.Error{Code: protocol.CodeInvalidTransition, Message: message, Details: details}
}
