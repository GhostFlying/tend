package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func recordIssueDeliveryDuty(ctx context.Context, tx *store.Tx, issue, task, event, acceptance string) error {
	// A delivery execution discharges its original obligation after acceptance
	// and provider verification; it does not manufacture an endless new handoff.
	_, err := tx.Exec(ctx, `INSERT INTO issue_delivery_duties(issue_id,task_id,event_id,acceptance_id)
 SELECT ?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM issue_delivery_executions WHERE task_id=?) ON CONFLICT DO NOTHING`, issue, task, event, acceptance, task)
	return err
}

// UpdateIssueDelivery records the concrete accepted-output handoff. It cannot
// mint an owner answer or perform provider mutations.
type UpdateIssueDelivery struct{}

func (UpdateIssueDelivery) Operation() string { return "issues.update_delivery" }
func (UpdateIssueDelivery) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (UpdateIssueDelivery) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "issue" || x.Env.ParentRequestID != "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "Issue target required")
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	issue, err := store.GetCanonicalIssue(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	if issue.Revision != expected {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "Issue changed")
	}
	var p protocol.UpdateIssueDeliveryPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if err := validateCanonicalSourceReference(ctx, tx.Q(), canonicalOwnerIssueSource(p.IssueSourceReference)); err != nil {
		return Outcome{}, err
	}
	if issue.AssignmentEpoch != p.ExpectedAssignmentEpoch || issue.AssignmentState != "active" {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "Issue assignment changed")
	}
	if issue.State != protocol.IssueActive && issue.State != protocol.IssuePaused && !(issue.State == protocol.IssueCancelling && p.Action != "prepare" && p.Action != "resume") {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "reopen responsibility before updating delivery")
	}
	if strings.TrimSpace(p.Reason) == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "delivery reason is required")
	}
	d, err := store.GetIssueDeliveryDuty(ctx, tx.Q(), issue.IssueID, p.TaskID)
	if err != nil {
		return Outcome{}, err
	}
	if d.Status == "complete" || d.Status == "report" || d.Status == "ended" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "delivery responsibility is already settled")
	}
	var events []Event
	var evidence []protocol.IssueEvidence
	switch p.Action {
	case "prepare", "resume":
		if p.Action == "resume" {
			if d.Status != "deferred" {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "resume requires an existing owner deferral")
			}
			if x.Principal.Kind != protocol.PrincipalOwner {
				if p.SourceMessageID == "" {
					return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "resume a deferred delivery only from a current admitted owner source")
				}
				if err := validateAttentionOwnerRelay(ctx, tx.Q(), x, issue.IssueID, p.SourceMessageID); err != nil {
					return Outcome{}, err
				}
			}
			if p.Candidate == nil {
				p.Candidate = d.Candidate
			}
		}
		if p.Candidate == nil {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "exact reviewable candidate is required")
		}
		if err := validateDeliveryCandidate(ctx, tx.Q(), issue, *p.Candidate); err != nil {
			return Outcome{}, err
		}
		if d.ExecutionTaskID != "" {
			task, err := store.GetTask(ctx, tx.Q(), d.ExecutionTaskID)
			if err != nil {
				return Outcome{}, err
			}
			var busy bool
			if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r LEFT JOIN leases l USING(run_id) WHERE r.task_id=? AND (r.observed_state IN ('starting','running','waiting','interrupted','unknown') OR l.status<>'released')) OR EXISTS(SELECT 1 FROM workspaces WHERE occupied_by=?)`, task.TaskID, task.TaskID).Scan(&busy); err != nil {
				return Outcome{}, err
			}
			if !task.Lifecycle.Terminal() || busy {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "reconcile the existing delivery execution and resource before changing its candidate")
			}
			var previousUpgrade string
			if err := tx.Q().QueryRowContext(ctx, `SELECT upgrade_id FROM issue_delivery_executions WHERE task_id=?`, task.TaskID).Scan(&previousUpgrade); err != nil {
				return Outcome{}, err
			}
			if previousUpgrade != "" {
				batch, err := store.GetUpgrade(ctx, tx.Q(), previousUpgrade)
				if err != nil {
					return Outcome{}, err
				}
				if batch.View.Status == "running" || batch.View.Status == "attention" {
					return Outcome{}, protocol.Errorf(protocol.CodeUnknownOutcome, "reconcile the original controlled upgrade before preparing another execution")
				}
			}
			// Explicit prepare is a new reviewed candidate after the old execution
			// settled. Keep its task/spec/run and authorization rows immutable.
			if reflect.DeepEqual(d.Candidate, p.Candidate) {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "this candidate already has a settled execution; reconcile its result before requesting another candidate")
			}
			d.ExecutionTaskID = ""
		}
		if d.AttentionID != "" {
			old, err := store.GetAttention(ctx, tx.Q(), d.AttentionID)
			if err != nil {
				return Outcome{}, err
			}
			decision, err := store.GetDecision(ctx, tx.Q(), old.DecisionID)
			if err != nil {
				return Outcome{}, err
			}
			if p.Action != "resume" && d.Status != "preparing" && reflect.DeepEqual(d.Candidate, p.Candidate) && decision.Question == deliveryCandidateQuestion(*p.Candidate) && ((decision.Status == store.DecisionOpen && decision.Version == 1) || (decision.Status == store.DecisionAnswered && decision.Version == 2)) {
				return Outcome{Result: d}, nil
			}
			if err := withdrawDeliveryQuestion(ctx, x, tx, &old); err != nil {
				return Outcome{}, err
			}
			events = append(events, attentionEvent(old))
		}

		inner := *x
		env := *x.Env
		inner.Env = &env
		env.Target = protocol.Target{Type: "attention"}
		env.ExpectedControlRevision = nil
		env.Payload, _ = json.Marshal(protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "decision", Title: deliveryCandidateQuestion(*p.Candidate), Options: []string{"approve", "defer", "end_delivery"}, BlocksClose: true})
		out, err := (AttentionCommand{Action: "create"}).Apply(ctx, &inner, tx)
		if err != nil {
			return Outcome{}, err
		}
		a := out.Result.(protocol.Attention)
		d.Candidate, d.AttentionID, d.Status = p.Candidate, a.AttentionID, "awaiting_owner"
		evidence = nil
		events = append(events, out.Events...)
	case "report":
		_, spec, err := loadTask(ctx, tx.Q(), d.TaskID)
		if err != nil {
			return Outcome{}, err
		}
		if spec.RequiresDelivery("commit") || d.Candidate != nil {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "accepted code delivery still needs its actual handoff or owner disposition")
		}
		// Bounded reports/reviews have a valid accepted endpoint, without merge.
		d.Status = "report"
		evidence = []protocol.IssueEvidence{{Kind: protocol.WorkItemEvidenceAcceptance, Reference: d.AcceptanceID, Summary: p.Reason}}
	case "end":
		reference := x.Env.RequestID
		if x.Principal.Kind != protocol.PrincipalOwner {
			option, err := deliveryOwnerOption(ctx, tx.Q(), d)
			if err != nil {
				return Outcome{}, err
			}
			reference = d.AttentionID
			if option != "end_delivery" {
				if p.SourceMessageID == "" {
					return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "only an actual owner disposition may end remaining delivery responsibility")
				}
				if err := validateAttentionOwnerRelay(ctx, tx.Q(), x, issue.IssueID, p.SourceMessageID); err != nil {
					return Outcome{}, err
				}
				reference = p.SourceMessageID
				if d.AttentionID != "" {
					a, err := store.GetAttention(ctx, tx.Q(), d.AttentionID)
					if err != nil {
						return Outcome{}, err
					}
					original, err := store.GetDecision(ctx, tx.Q(), a.DecisionID)
					if err != nil {
						return Outcome{}, err
					}
					if original.AnsweredMessageID == p.SourceMessageID {
						return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "the original answer did not end delivery; preserve its recorded option")
					}
				}
			}
		}
		if d.AttentionID != "" {
			old, err := store.GetAttention(ctx, tx.Q(), d.AttentionID)
			if err != nil {
				return Outcome{}, err
			}
			if err := withdrawDeliveryQuestion(ctx, x, tx, &old); err != nil {
				return Outcome{}, err
			}
			events = append(events, attentionEvent(old))
		}
		evidence = []protocol.IssueEvidence{{Kind: protocol.WorkItemEvidenceOwnerDecision, Reference: reference, Summary: p.Reason}}
		d.Status = "ended"
	case "complete":
		if d.ExecutionTaskID == "" || d.Candidate == nil {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "accepted delivery execution is required")
		}
		task, err := store.GetTask(ctx, tx.Q(), d.ExecutionTaskID)
		if err != nil {
			return Outcome{}, err
		}
		if task.Lifecycle != protocol.TaskCompleted {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "delivery execution must pass independent acceptance")
		}
		obs, err := store.GetDeliveryObservation(ctx, tx.Q(), issue.IssueID, d.Candidate.DeliveryID)
		if err != nil {
			return Outcome{}, err
		}
		if obs.Observation.ErrorKind != "" || obs.Observation.RefreshPending || obs.Observation.State != "merged" || obs.Observation.HeadSHA != d.Candidate.HeadSHA || obs.Observation.MergeSHA == "" || (d.Candidate.MergeSHA != "" && obs.Observation.MergeSHA != d.Candidate.MergeSHA) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "verified matching provider merge is required")
		}
		accs, err := store.ListAcceptances(ctx, tx.Q(), task.TaskID, task.CurrentArtifactID)
		if err != nil {
			return Outcome{}, err
		}
		evidence = nil
		for _, a := range accs {
			if a.SpecVersion == task.SpecVersion && a.Verdict == protocol.VerdictPassed {
				evidence = []protocol.IssueEvidence{{Kind: protocol.WorkItemEvidenceAcceptance, Reference: a.AcceptanceID, Summary: p.Reason}}
			}
		}
		if len(evidence) == 0 {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "matching delivery acceptance is required")
		}
		evidence = append(evidence, protocol.IssueEvidence{Kind: protocol.WorkItemEvidenceDelivery, Reference: d.Candidate.DeliveryID, Summary: "Verified provider merge of the approved head", Observation: &obs.Observation})
		if d.Candidate.Deploy {
			if d.Candidate.Deployment == nil {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "prepare and approve explicit deployment targets before completing delivery")
			}
			var bound string
			if err := tx.Q().QueryRowContext(ctx, `SELECT upgrade_id FROM issue_delivery_executions WHERE task_id=?`, task.TaskID).Scan(&bound); err != nil {
				return Outcome{}, err
			}
			if bound == "" || bound != p.UpgradeID {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "completion must verify the upgrade batch bound by this execution's artifact")
			}
			proof, err := deliveryUpgradeEvidence(ctx, x, tx.Q(), *d.Candidate.Deployment, p.UpgradeID, task.CreatedAt)
			if err != nil {
				return Outcome{}, err
			}
			evidence = append(evidence, protocol.IssueEvidence{Kind: protocol.WorkItemEvidenceUpgrade, Reference: p.UpgradeID, Summary: "Verified controlled self-update and target installations after the approved execution began", Upgrade: proof})
		} else if p.UpgradeID != "" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "merge-only approval does not authorize deployment")
		}
		d.Status = "complete"
	default:
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "delivery action must be prepare, resume, report, end or complete")
	}
	d.Revision++
	d.Reason, d.Evidence = p.Reason, evidence
	if err := tx.PutIssueDeliveryDuty(ctx, issue.IssueID, d); err != nil {
		return Outcome{}, err
	}
	issue.Revision++
	issue.UpdatedAt = x.Now.UnixMilli()
	out, err := saveCanonicalOwnerDefinition(ctx, x, tx, issue, expected, canonicalOwnerIssueSource(p.IssueSourceReference), p, "issue.delivery_updated")
	if err != nil {
		return Outcome{}, err
	}
	out.Result = d
	out.Events = append(out.Events, events...)
	return out, nil
}

func deliveryCandidateQuestion(c protocol.IssueDeliveryCandidate) string {
	mergeScope := "merge authorized on approve"
	if c.ProviderState == "merged" {
		mergeScope = "existing merge " + c.MergeSHA + "; deployment only; never merge again"
	}
	return fmt.Sprintf("Approve this delivery candidate and scope? PR %s; head %s; base %s; provider %s; validation %s; %s; deploy=%t; deployment scope: %s. Defer retains responsibility without merge/deploy; end_delivery explicitly ends remaining delivery responsibility without integration. A changed candidate needs a new question.", c.URL, c.HeadSHA, c.BaseSHA, c.ProviderState, c.Validation, mergeScope, c.Deploy, c.DeploymentScope) + deliveryDeploymentQuestion(c)
}
func validateDeliveryCandidate(ctx context.Context, q store.Querier, issue protocol.Issue, c protocol.IssueDeliveryCandidate) error {
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || !validDeliverySHA(c.HeadSHA) || !validDeliverySHA(c.BaseSHA) || strings.TrimSpace(c.Validation) == "" || strings.TrimSpace(c.DeploymentScope) == "" || (c.ProviderState != "open" && !(c.ProviderState == "merged" && c.Deploy && validDeliverySHA(c.MergeSHA))) || (c.ProviderState == "open" && c.MergeSHA != "") {
		return protocol.Errorf(protocol.CodeInvalidRequest, "candidate requires a real HTTPS PR, head/base SHA, validation, known open candidate or verified merged deployment-only candidate and explicit deployment scope (including none)")
	}
	if c.Deploy {
		if c.Deployment == nil || strings.TrimSpace(c.Deployment.ServerInstance) == "" || c.Deployment.HostIDs == nil {
			return protocol.Errorf(protocol.CodeInvalidRequest, "deployment requires an explicit server instance and complete host set")
		}
		seen := map[string]bool{}
		for _, host := range c.Deployment.HostIDs {
			if strings.TrimSpace(host) == "" || seen[host] {
				return protocol.Errorf(protocol.CodeInvalidRequest, "deployment host set must contain unique nonempty identities")
			}
			seen[host] = true
		}
	} else if c.Deployment != nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "merge-only candidate cannot include deployment targets")
	}
	found := false
	for _, ref := range issue.Plan.Deliveries {
		if ref.DeliveryID == c.DeliveryID && ref.URL == c.URL {
			found = true
		}
	}
	if !found {
		return protocol.Errorf(protocol.CodeInvalidRequest, "candidate must identify this Issue's registered PR delivery")
	}
	obs, err := store.GetDeliveryObservation(ctx, q, issue.IssueID, c.DeliveryID)
	if err != nil {
		return err
	}
	if obs.Observation.ErrorKind != "" || obs.Observation.RefreshPending || obs.Observation.CheckedAt == 0 || obs.Observation.State != c.ProviderState || obs.Observation.HeadSHA != c.HeadSHA || obs.Observation.BaseSHA != c.BaseSHA || (c.ProviderState == "merged" && obs.Observation.MergeSHA != c.MergeSHA) {
		return protocol.Errorf(protocol.CodeStaleRevision, "provider candidate is unknown or changed; refresh and prepare the current candidate")
	}
	return nil
}
func validDeliverySHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func deliveryOwnerOption(ctx context.Context, q store.Querier, d protocol.IssueDeliveryDuty) (string, error) {
	if d.Candidate == nil || d.AttentionID == "" {
		return "", nil
	}
	a, err := store.GetAttention(ctx, q, d.AttentionID)
	if err != nil {
		return "", err
	}
	decision, err := store.GetDecision(ctx, q, a.DecisionID)
	if err != nil {
		return "", err
	}
	if decision.Status != store.DecisionAnswered || decision.Version != 2 || decision.Question != deliveryCandidateQuestion(*d.Candidate) {
		return "", nil
	}
	// Extra free text can change the scope. Retain it for coordination, never
	// silently execute an old option with additional requirements.
	var answer decisionAnswer
	if err := json.Unmarshal([]byte(decision.AnswerJSON), &answer); err != nil {
		return "", err
	}
	if answer.Text != "" {
		return "", nil
	}
	var sourceCurrent bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_message_controls c USING(message_id) LEFT JOIN message_dispositions d USING(message_id) WHERE m.message_id=? AND m.author='owner' AND COALESCE(c.withdrawn_at,0)=0 AND COALESCE(d.content_revision,1)=(SELECT content_revision FROM issue_delivery_answers WHERE decision_id=? AND message_id=m.message_id))`, decision.AnsweredMessageID, decision.DecisionID).Scan(&sourceCurrent)
	if err != nil || !sourceCurrent {
		return "", err
	}
	return answer.Option, nil
}

func deliveryExecutionAuthorized(ctx context.Context, q store.Querier, issue protocol.Issue, auth protocol.IssueDeliveryAuthorization) (protocol.IssueDeliveryDuty, error) {
	d, err := store.GetIssueDeliveryDuty(ctx, q, issue.IssueID, auth.TaskID)
	if err != nil {
		return d, err
	}
	if d.Revision != auth.Revision || (d.Status != "awaiting_owner" && d.Status != "executing") {
		return d, protocol.Errorf(protocol.CodeStaleRevision, "delivery candidate changed")
	}
	option, err := deliveryOwnerOption(ctx, q, d)
	if err != nil {
		return d, err
	}
	if option != "approve" {
		return d, protocol.Errorf(protocol.CodeForbidden, "delivery needs the original versioned owner approval")
	}
	if err := validateDeliveryCandidate(ctx, q, issue, *d.Candidate); err != nil {
		return d, err
	}
	return d, nil
}

func deliveryTaskCurrent(ctx context.Context, q store.Querier, task string) (bool, error) {
	var issue, source string
	var revision int64
	err := q.QueryRowContext(ctx, `SELECT issue_id,source_task_id,duty_revision FROM issue_delivery_executions WHERE task_id=?`, task).Scan(&issue, &source, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}

	if err != nil {
		return false, err
	}
	i, err := store.GetCanonicalIssue(ctx, q, issue)
	if err != nil {
		return false, err
	}
	_, err = deliveryExecutionAuthorized(ctx, q, i, protocol.IssueDeliveryAuthorization{TaskID: source, Revision: revision})
	if err != nil && (protocol.CodeOf(err) == protocol.CodeForbidden || protocol.CodeOf(err) == protocol.CodeStaleRevision || protocol.CodeOf(err) == protocol.CodeInvalidRequest || errors.Is(err, store.ErrNotFound)) {
		return false, nil
	}
	return err == nil, err
}

func withdrawDeliveryQuestion(ctx context.Context, x *Exec, tx *store.Tx, a *protocol.Attention) error {
	if a.Status != "open" {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE decisions SET status='cancelled',version=version+1,updated_at=? WHERE decision_id=? AND status='open'`, x.Now.UnixMilli(), a.DecisionID); err != nil {
		return err
	}
	a.Status, a.Resolution = "withdrawn", "Delivery candidate superseded or explicitly ended"
	return saveAttention(ctx, x, tx, a, a.Revision)
}

// invalidateIssueDeliveryCandidates remembers observed drift, including a
// temporary failure followed by an identical snapshot. Old answers never become
// live again merely because the provider later returns to its previous state.
func invalidateIssueDeliveryCandidates(ctx context.Context, x *Exec, tx *store.Tx, o protocol.DeliveryObservation) ([]Event, error) {
	duties, err := store.ListIssueDeliveryDuties(ctx, tx.Q(), o.WorkItemID)
	if err != nil {
		return nil, err
	}
	var events []Event
	for _, d := range duties {
		c := d.Candidate
		if c == nil || c.DeliveryID != o.DeliveryID || (d.Status != "awaiting_owner" && d.Status != "deferred" && d.Status != "executing") {
			continue
		}
		if o.ErrorKind == "" && o.HeadSHA == c.HeadSHA && (c.MergeSHA == "" || o.MergeSHA == c.MergeSHA) && ((o.State == c.ProviderState && o.BaseSHA == c.BaseSHA) || (d.Status == "executing" && o.State == "merged")) {
			continue
		}
		if d.AttentionID != "" {
			a, err := store.GetAttention(ctx, tx.Q(), d.AttentionID)
			if err != nil {
				return nil, err
			}
			if a.Status == "open" {
				if err := withdrawDeliveryQuestion(ctx, x, tx, &a); err != nil {
					return nil, err
				}
				events = append(events, attentionEvent(a))
			}
		}
		d.Status = "preparing"
		d.Revision++
		d.Reason = "Provider identity or outcome changed; reconcile and prepare a current versioned candidate"
		if err := tx.PutIssueDeliveryDuty(ctx, o.WorkItemID, d); err != nil {
			return nil, err
		}
		events = append(events, Event{ObjectType: "issue", ObjectID: o.WorkItemID, Kind: "issue.delivery_candidate_expired", Body: map[string]any{"task_id": d.TaskID, "delivery_id": o.DeliveryID, "duty_revision": d.Revision}})
	}
	return events, nil
}

func deliveryDeploymentQuestion(c protocol.IssueDeliveryCandidate) string {
	if c.Deployment == nil {
		return ""
	}
	return fmt.Sprintf(" Tend executes the controlled self-update itself: server instance %s; complete host set %v; build from the verified merge and read back installed server/hosts and this original Issue before independent acceptance.", c.Deployment.ServerInstance, c.Deployment.HostIDs)
}

func deliveryUpgradeBinding(ctx context.Context, q store.Querier, target protocol.IssueDeliveryDeployment, id string, executionStarted int64) (store.UpgradeRow, error) {
	if id == "" {
		return store.UpgradeRow{}, protocol.Errorf(protocol.CodeInvalidTransition, "approved deployment requires its controlled upgrade batch")
	}
	r, err := store.GetUpgrade(ctx, q, id)
	if err != nil {
		return store.UpgradeRow{}, errNotFound(err, "controlled delivery upgrade does not exist")
	}
	hosts := make([]string, 0, len(r.Hosts))
	for _, h := range r.Hosts {
		hosts = append(hosts, h.HostID)
	}
	slices.Sort(hosts)
	want := slices.Clone(target.HostIDs)
	slices.Sort(want)
	if r.View.CreatedAt.UnixMilli() < executionStarted || r.ServerInstance != target.ServerInstance || !slices.Equal(hosts, want) {
		return store.UpgradeRow{}, protocol.Errorf(protocol.CodeInvalidTransition, "upgrade must belong to this execution's approved server and complete host set")
	}
	return r, nil
}

func deliveryUpgradeEvidence(ctx context.Context, x *Exec, q store.Querier, target protocol.IssueDeliveryDeployment, id string, executionStarted int64) (*protocol.UpgradeEvidence, error) {
	r, err := deliveryUpgradeBinding(ctx, q, target, id, executionStarted)
	if err != nil {
		return nil, err
	}
	if r.ServerProcess == "" || r.ServerHealthy < 2 {
		return nil, protocol.Errorf(protocol.CodeInvalidTransition, "controlled delivery requires verified installed server identity and health")
	}
	proof, err := workItemUpgradeEvidence(ctx, x, q, id)
	if err != nil {
		return nil, err
	}
	proof.ServerInstance, proof.ServerProcessInstanceID = r.ServerInstance, r.ServerProcess
	return proof, nil
}
