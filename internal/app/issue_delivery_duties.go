package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func recordIssueDeliveryDuty(ctx context.Context, x *Exec, tx *store.Tx, issue, task, event, acceptance string) error {
	// A delivery execution discharges its original obligation after acceptance
	// and provider verification; it does not manufacture an endless new handoff.
	var accepted, artifact any
	if acceptance != "" {
		accepted = acceptance
	} else {
		row, err := store.GetTask(ctx, tx.Q(), task)
		if err != nil {
			return err
		}
		artifact = row.CurrentArtifactID
	}
	result, err := tx.Exec(ctx, `INSERT INTO issue_delivery_duties(issue_id,task_id,event_id,acceptance_id,artifact_id)
 SELECT ?,?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM issue_delivery_executions WHERE task_id=?) ON CONFLICT DO NOTHING`, issue, task, event, accepted, artifact, task)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return err
	}
	// Completion already schedules its existing owner notice. This event only
	// indexes the actual newly persisted duty in the same transaction.
	change := deliveryDutyContextEvent(issue, protocol.IssueDeliveryDuty{TaskID: task, Revision: 1, Status: "preparing"})
	body, err := json.Marshal(change.Body)
	if err != nil {
		return err
	}
	actor, err := json.Marshal(x.ActualPrincipal())
	if err != nil {
		return err
	}
	_, err = tx.InsertEvent(ctx, store.EventRow{EventID: x.IDs.New(protocol.PrefixEvent), Source: "app", ObjectType: change.ObjectType, ObjectID: change.ObjectID, Kind: change.Kind, BodyJSON: string(body), RequestID: x.Env.RequestID, TraceID: x.TraceID, ActorJSON: string(actor), CreatedAt: x.Now.UnixMilli()})
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
	if issue.State != protocol.IssueActive && issue.State != protocol.IssuePaused && !(issue.State == protocol.IssueCancelling && p.Action != "prepare" && p.Action != "resume" && p.Action != "bind") {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "reopen responsibility before updating delivery")
	}
	if strings.TrimSpace(p.Reason) == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "delivery reason is required")
	}
	if p.Action == "submit_result" {
		return submitIssueOwnerResult(ctx, x, tx, issue, p)
	}
	if p.AcceptanceID != "" {
		if err := bindIssueOwnerResultAcceptance(ctx, x, tx, issue, p); err != nil {
			return Outcome{}, err
		}
	}
	if p.ReviewerArtifactID != "" {
		if err := bindIssueOwnerResultReview(ctx, tx, issue, p); err != nil {
			return Outcome{}, err
		}
	}
	d, err := loadOrPrepareLiveDeliveryDuty(ctx, tx, issue, p)
	if err != nil {
		return Outcome{}, err
	}
	if d.Status == "complete" || d.Status == "report" || d.Status == "ended" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "delivery responsibility is already settled")
	}
	var events []Event
	var evidence []protocol.IssueEvidence
	switch p.Action {
	case "bind":
		if err := bindLiveDeliveryTask(ctx, tx, issue, d, p); err != nil {
			return Outcome{}, err
		}
		d.ExecutionTaskID, d.Status = p.ExecutionTaskID, "executing"
	case "prepare", "resume":
		// Deferral is an owner disposition independent of candidate freshness.
		// Preparing or observing a different candidate cannot reopen confirmation.
		if p.Action == "prepare" && d.Status == "deferred" {
			return Outcome{Result: d}, nil
		}
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
			if p.SourceMessageID != "" && d.AttentionID != "" {
				a, err := store.GetAttention(ctx, tx.Q(), d.AttentionID)
				if err != nil {
					return Outcome{}, err
				}
				decision, err := store.GetDecision(ctx, tx.Q(), a.DecisionID)
				if err != nil {
					return Outcome{}, err
				}
				var later bool
				if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_messages source JOIN conversation_messages answer ON answer.message_id=? WHERE source.message_id=? AND source.seq>answer.seq)`, decision.AnsweredMessageID, p.SourceMessageID).Scan(&later); err != nil {
					return Outcome{}, err
				}
				if !later {
					return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "resume requires a new owner source after the defer answer")
				}
			}
			if p.Candidate == nil {
				p.Candidate = d.Candidate
			}
		}
		if p.Candidate == nil {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "exact reviewable candidate is required")
		}
		if err := normalizeDeliveryBaseline(ctx, tx.Q(), p.Candidate); err != nil {
			return Outcome{}, err
		}
		if _, _, err := checkDeliveryReconciliation(ctx, tx.Q(), issue.IssueID, p.Candidate); err != nil {
			return Outcome{}, err
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
		policyIssue, policy, err := store.EffectiveIssuePRPolicy(ctx, tx.Q(), issue)
		if err != nil {
			return Outcome{}, err
		}
		policyCovers := policy != nil && policy.Mode == "auto" && store.IssuePolicyAuthorizesDeployment(policy, p.Candidate)
		recoverManualQuestion := false
		if d.AttentionID != "" {
			old, err := store.GetAttention(ctx, tx.Q(), d.AttentionID)
			if err != nil {
				return Outcome{}, err
			}
			decision, err := store.GetDecision(ctx, tx.Q(), old.DecisionID)
			if err != nil {
				return Outcome{}, err
			}
			if p.Action != "resume" && d.AssignmentEpoch == issue.AssignmentEpoch && d.Status != "preparing" && reflect.DeepEqual(d.Candidate, p.Candidate) && decision.Question == deliveryCandidateQuestion(*p.Candidate) && ((decision.Status == store.DecisionOpen && decision.Version == 1) || (decision.Status == store.DecisionAnswered && decision.Version == 2)) {
				if decision.Status == store.DecisionOpen && !policyCovers {
					return Outcome{Result: d}, nil
				}
				if decision.Status == store.DecisionAnswered {
					option, err := deliveryOwnerOption(ctx, tx.Q(), d)
					if err != nil {
						return Outcome{}, err
					}
					if oneOf(option, "approve", "defer", "end_delivery") {
						return Outcome{Result: d}, nil
					}
				}
				// Recover the original concrete confirmation, not an automatic
				// grant introduced by a later policy change.
				recoverManualQuestion = !policyCovers
				if d.AcceptanceID == "" && d.ArtifactID == "" {
					if _, err := currentLiveDeliveryTask(ctx, tx.Q(), issue.IssueID, d.TaskID, p.ExpectedTaskControlRevision); err != nil {
						return Outcome{}, err
					}
				}
			}
			if err := withdrawDeliveryQuestion(ctx, x, tx, &old); err != nil {
				return Outcome{}, err
			}
			events = append(events, attentionEvent(old))
		}

		if policy == nil && !recoverManualQuestion {
			if d.PolicyAttentionID != "" && reflect.DeepEqual(d.Candidate, p.Candidate) && d.AssignmentEpoch == issue.AssignmentEpoch {
				return Outcome{Result: d}, nil
			}
			repository := ""
			for _, ref := range issue.Plan.Deliveries {
				if ref.DeliveryID == p.Candidate.DeliveryID {
					repository = ref.Repository
				}
			}
			attention, policyEvents, err := ensureIssuePRPolicyQuestion(ctx, x, tx, issue, repository)
			if err != nil {
				return Outcome{}, err
			}
			d.Candidate, d.AssignmentEpoch, d.Status, d.PolicyAttentionID = p.Candidate, issue.AssignmentEpoch, "preparing", attention
			d.Authorization = nil
			d.AttentionID = ""
			d.Reason = p.Reason
			d.Revision++
			if err := tx.PutIssueDeliveryDuty(ctx, issue.IssueID, d); err != nil {
				return Outcome{}, err
			}
			out, err := saveCanonicalOwnerDefinition(ctx, x, tx, issueWithNextRevision(issue, x.Now.UnixMilli()), issue.Revision, canonicalOwnerIssueSource(p.IssueSourceReference), p, "issue.delivery_updated")
			contextDeliveryEventDetails(&out, p.TaskID)
			out.Result = d
			out.Events = append(out.Events, events...)
			out.Events = append(out.Events, policyEvents...)
			return out, err
		}
		d.PolicyAttentionID = ""
		if !recoverManualQuestion && policyCovers {
			reviewDuty := d
			reviewDuty.Candidate = p.Candidate
			if err := store.RequireDeliveryDutyReview(ctx, tx.Q(), issue, reviewDuty); err != nil {
				return Outcome{}, err
			}
			if err := store.RequireDeliveryChecks(ctx, tx.Q(), issue, *p.Candidate); err != nil {
				return Outcome{}, err
			}
			if d.Status == "authorized" && d.AssignmentEpoch == issue.AssignmentEpoch && reflect.DeepEqual(d.Candidate, p.Candidate) && store.DeliveryGrantValid(ctx, tx.Q(), issue, d) == nil {
				return Outcome{Result: d}, nil
			}
			grant := &protocol.IssueDeliveryGrant{Mode: "auto", PolicyIssueID: policyIssue.IssueID, PolicyRevision: policyIssue.Revision, PolicyDigest: store.IssuePRPolicyDigest(policy)}
			if policy.Authority != nil {
				grant.RequestID = policy.Authority.RequestID
			}
			d.Candidate, d.Authorization, d.AttentionID, d.AssignmentEpoch, d.Status = p.Candidate, grant, "", issue.AssignmentEpoch, "authorized"
			if err := store.DeliveryGrantValid(ctx, tx.Q(), issue, d); err != nil {
				return Outcome{}, err
			}
			d.Revision++
			d.Reason = p.Reason
			if err := tx.PutIssueDeliveryDuty(ctx, issue.IssueID, d); err != nil {
				return Outcome{}, err
			}
			out, err := saveCanonicalOwnerDefinition(ctx, x, tx, issueWithNextRevision(issue, x.Now.UnixMilli()), issue.Revision, canonicalOwnerIssueSource(p.IssueSourceReference), p, "issue.delivery_updated")
			contextDeliveryEventDetails(&out, p.TaskID)
			out.Result = d
			out.Events = append(out.Events, events...)
			return out, err
		}
		if policy != nil {
			reviewDuty := d
			reviewDuty.Candidate = p.Candidate
			if err := store.RequireDeliveryDutyReview(ctx, tx.Q(), issue, reviewDuty); err != nil {
				return Outcome{}, err
			}
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
		d.Candidate, d.AttentionID, d.Status, d.AssignmentEpoch = p.Candidate, a.AttentionID, "awaiting_owner", issue.AssignmentEpoch
		d.Authorization = &protocol.IssueDeliveryGrant{Mode: "manual"}
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
		basis, err := completedTaskEvidence(ctx, tx.Q(), d.TaskID, p.Reason)
		if err != nil {
			return Outcome{}, err
		}
		evidence = []protocol.IssueEvidence{basis}
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
		if p.ExecutionTaskID != "" && p.ExecutionTaskID != d.ExecutionTaskID {
			evidence, err = contributionDeliverySettlementEvidence(ctx, tx.Q(), issue.IssueID, d, p)
			if err != nil {
				return Outcome{}, err
			}
			d.ExecutionTaskID, d.Status = p.ExecutionTaskID, "complete"
			break
		}
		if d.ExecutionTaskID == "" || d.Candidate == nil {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "accepted delivery execution is required")
		}
		if d.Candidate.Deploy {
			settlement, err := store.DeliveryDeploymentSettlement(ctx, tx.Q(), d.ExecutionTaskID, x.Now.UnixMilli())
			if err != nil {
				return Outcome{}, err
			}
			if settlement == nil || !settlement.Eligible || p.UpgradeID != settlement.UpgradeID {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "settlement must retain the original execution's completed controlled upgrade")
			}
			basis := protocol.IssueEvidence{Kind: protocol.WorkItemEvidenceAcceptance, Reference: settlement.AcceptanceID, Summary: p.Reason}
			if settlement.AcceptanceID == "" {
				basis.Kind, basis.Reference = protocol.WorkItemEvidenceResult, settlement.ArtifactID
			}
			evidence = []protocol.IssueEvidence{basis}
			// This is the real current observation, not a manufactured historical
			// provider snapshot. The bound upgrade proves the installed source.
			observation, err := store.GetDeliveryObservation(ctx, tx.Q(), issue.IssueID, d.Candidate.DeliveryID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return Outcome{}, err
			}
			if err == nil && observation.Observation.CheckedAt > 0 && oneOf(observation.Observation.State, "open", "merged", "closed") && observation.Observation.ErrorKind == "" && !observation.Observation.RefreshPending {
				evidence = append(evidence, protocol.IssueEvidence{Kind: protocol.WorkItemEvidenceDelivery, Reference: d.Candidate.DeliveryID, Summary: "Recorded provider facts; the bound upgrade proves the original installed source", Observation: &observation.Observation})
			}
			if d.Candidate.Reconciliation != nil {
				original, _, err := checkDeliveryReconciliation(ctx, tx.Q(), issue.IssueID, d.Candidate)
				if err != nil {
					return Outcome{}, err
				}
				evidence = append(evidence, original)
			}
			evidence = append(evidence, protocol.IssueEvidence{Kind: protocol.WorkItemEvidenceUpgrade, Reference: settlement.UpgradeID, Summary: p.Reason, Upgrade: settlement.Upgrade})
			d.Status = "complete"
			break
		}
		if d.AssignmentEpoch != issue.AssignmentEpoch {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "delivery owner epoch changed; reconcile the original facts under a fresh candidate")
		}
		var executionRevision int64
		if err := tx.Q().QueryRowContext(ctx, `SELECT duty_revision FROM issue_delivery_executions WHERE task_id=? AND issue_id=? AND source_task_id=?`, d.ExecutionTaskID, issue.IssueID, d.TaskID).Scan(&executionRevision); err != nil {
			return Outcome{}, errNotFound(err, "bound delivery execution does not exist")
		}
		if d.Status != "executing" || d.Revision != executionRevision {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "delivery candidate expired; reconcile the original execution before settling responsibility")
		}
		if err := store.DeliveryGrantValid(ctx, tx.Q(), issue, d); err != nil {
			return Outcome{}, err
		}
		if d.Authorization != nil {
			_, policy, err := store.EffectiveIssuePRPolicy(ctx, tx.Q(), issue)
			if err != nil {
				return Outcome{}, err
			}
			if policy != nil {
				if err := store.RequireDeliveryDutyReview(ctx, tx.Q(), issue, d); err != nil {
					return Outcome{}, err
				}
			}
		}
		task, err := store.GetTask(ctx, tx.Q(), d.ExecutionTaskID)
		if err != nil {
			return Outcome{}, err
		}
		if task.Lifecycle != protocol.TaskCompleted && task.Kind != protocol.TaskKindIssueManager {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "delivery execution must complete under its frozen acceptance policy")
		}
		obs, err := store.GetDeliveryObservation(ctx, tx.Q(), issue.IssueID, d.Candidate.DeliveryID)
		if err != nil {
			return Outcome{}, err
		}
		if x.serverDeliveryObservations && obs.Observation.Observer != "server" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "request issues.refresh_delivery and await the bounded server observation before completing this existing execution; historical runner facts do not establish current server verification")
		}
		if obs.Observation.ErrorKind != "" || obs.Observation.RefreshPending || obs.Observation.State != "merged" || obs.Observation.HeadSHA != d.Candidate.HeadSHA || obs.Observation.MergeSHA == "" || (d.Candidate.MergeSHA != "" && obs.Observation.MergeSHA != d.Candidate.MergeSHA) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "verified matching provider merge is required")
		}
		// Acceptance verifies the frozen artifact, not the provider's current
		// integration. Merge-only delivery must retain the same base/target and
		// lineage guarantees as deployment, including for historical observations.
		if _, err := deliveryDeploymentSource(*d.Candidate, obs.Observation); err != nil {
			return Outcome{}, err
		}
		basis, err := completedTaskEvidence(ctx, tx.Q(), task.TaskID, p.Reason)
		if err != nil {
			return Outcome{}, err
		}
		evidence = []protocol.IssueEvidence{basis}
		evidence = append(evidence, protocol.IssueEvidence{Kind: protocol.WorkItemEvidenceDelivery, Reference: d.Candidate.DeliveryID, Summary: "Verified provider merge of the approved head", Observation: &obs.Observation})
		if p.UpgradeID != "" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "merge-only approval does not authorize deployment")
		}
		d.Status = "complete"
	default:
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "delivery action must be submit_result, prepare, resume, bind, report, end or complete")
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
	contextDeliveryEventDetails(&out, p.TaskID)
	out.Result = d
	out.Events = append(out.Events, events...)
	return out, nil
}

func deliveryCandidateQuestion(c protocol.IssueDeliveryCandidate) string {
	return protocol.DeliveryCandidateQuestion(c)
}
func validateDeliveryCandidate(ctx context.Context, q store.Querier, issue protocol.Issue, c protocol.IssueDeliveryCandidate) error {
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || !validDeliverySHA(c.HeadSHA) || !validDeliverySHA(c.BaseSHA) || !validDeliverySHA(c.TargetSHA) || strings.TrimSpace(c.Validation) == "" || strings.TrimSpace(c.DeploymentScope) == "" || (c.ProviderState != "open" && !(c.ProviderState == "merged" && c.Deploy && validDeliverySHA(c.MergeSHA))) || (c.ProviderState == "open" && c.MergeSHA != "") {
		return protocol.Errorf(protocol.CodeInvalidRequest, "candidate requires a real HTTPS PR, head/base/target SHA, validation, known open candidate or verified merged deployment-only candidate and explicit deployment scope (including none)")
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
	if err := deliveryObservationBaseline(ctx, q, issue.IssueID, c); err != nil {
		return err
	}
	if _, _, err := checkDeliveryReconciliation(ctx, q, issue.IssueID, &c); err != nil {
		return err
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
	if obs.Observation.ErrorKind != "" || obs.Observation.RefreshPending || obs.Observation.CheckedAt == 0 || obs.Observation.State != c.ProviderState || obs.Observation.HeadSHA != c.HeadSHA || obs.Observation.BaseSHA != c.BaseSHA || obs.Observation.TargetSHA != c.TargetSHA || (c.ProviderState == "merged" && (obs.Observation.MergeSHA != c.MergeSHA || !obs.Observation.TargetContainsMerge)) {
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
	return store.DeliveryOwnerOption(ctx, q, d)
}

func deliveryExecutionAuthorized(ctx context.Context, q store.Querier, issue protocol.Issue, auth protocol.IssueDeliveryAuthorization) (protocol.IssueDeliveryDuty, error) {
	d, err := store.GetIssueDeliveryDuty(ctx, q, issue.IssueID, auth.TaskID)
	if err != nil {
		return d, err
	}
	if d.AssignmentEpoch != issue.AssignmentEpoch || d.Revision != auth.Revision || (d.Status != "awaiting_owner" && d.Status != "authorized" && d.Status != "executing") {
		return d, protocol.Errorf(protocol.CodeStaleRevision, "delivery candidate changed")
	}
	if err := store.DeliveryGrantValid(ctx, q, issue, d); err != nil {
		return d, err
	}
	if d.Authorization != nil && d.Candidate != nil {
		_, policy, err := store.EffectiveIssuePRPolicy(ctx, q, issue)
		if err != nil {
			return d, err
		}
		if policy != nil {
			if err := store.RequireDeliveryDutyReview(ctx, q, issue, d); err != nil {
				return d, err
			}
			if d.Status != "executing" || d.Candidate.ProviderState != "open" {
				if err := store.RequireDeliveryChecks(ctx, q, issue, *d.Candidate); err != nil {
					return d, err
				}
			} else {
				observed, err := store.GetDeliveryObservation(ctx, q, issue.IssueID, d.Candidate.DeliveryID)
				if err != nil {
					return d, err
				}
				if observed.Observation.State == "open" {
					if err := store.RequireDeliveryChecks(ctx, q, issue, *d.Candidate); err != nil {
						return d, err
					}
				}
			}
		}
	}
	if d.Status == "executing" && d.ExecutionTaskID != "" && d.Candidate.ProviderState == "open" {
		observed, err := store.GetDeliveryObservation(ctx, q, issue.IssueID, d.Candidate.DeliveryID)
		if err != nil {
			return d, err
		}
		if observed.Observation.State == "merged" {
			if _, err := deliveryDeploymentSource(*d.Candidate, observed.Observation); err != nil {
				return d, err
			}
			return d, nil
		}
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
	d, err := store.GetIssueDeliveryDuty(ctx, q, issue, source)
	if err != nil {
		return false, err
	}
	if d.AssignmentEpoch != i.AssignmentEpoch {
		return false, nil
	}
	if d.Candidate != nil && d.Candidate.Deploy {
		available, err := deliveryTargetAvailable(ctx, q, task)
		if err != nil || !available {
			return false, err
		}
		var bound string
		if err := q.QueryRowContext(ctx, `SELECT upgrade_id FROM issue_delivery_executions WHERE task_id=?`, task).Scan(&bound); err != nil {
			return false, err
		}
		if bound != "" && d.ExecutionTaskID == task && d.Revision == revision && d.Status == "executing" {
			batch, err := store.GetUpgrade(ctx, q, bound)
			if err != nil {
				return false, err
			}
			option, err := deliveryOwnerOption(ctx, q, d)
			if err != nil {
				return false, err
			}
			return batch.View.Status == "completed" && option == "approve", nil
		}
	}
	_, err = deliveryExecutionAuthorized(ctx, q, i, protocol.IssueDeliveryAuthorization{TaskID: source, Revision: revision})
	if err != nil && (protocol.CodeOf(err) == protocol.CodeForbidden || protocol.CodeOf(err) == protocol.CodeStaleRevision || protocol.CodeOf(err) == protocol.CodeInvalidRequest || protocol.CodeOf(err) == protocol.CodeUnknownOutcome || protocol.CodeOf(err) == protocol.CodeInvalidTransition || errors.Is(err, store.ErrNotFound)) {
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

func deliveryUpgradeBinding(ctx context.Context, q store.Querier, target protocol.IssueDeliveryDeployment, id string, executionStarted int64) (store.UpgradeRow, error) {
	r, err := store.DeliveryUpgradeBinding(ctx, q, target, id, executionStarted)
	return r, errNotFound(err, "controlled delivery upgrade does not exist")
}

// Candidate expiry is a responsibility input, including when provider text is
// unchanged after another deployment. It is not another human approval request.
func recordDeliveryCandidateExpiry(ctx context.Context, x *Exec, tx *store.Tx, issue, event string) error {
	conversation, err := ensureCanonicalManagerConversationForWake(ctx, x, tx, issue)
	if err != nil || conversation == "" {
		return err
	}
	message := x.IDs.New(protocol.PrefixMessage)
	if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: message, ConversationID: conversation, Author: "system", Body: "The delivery candidate or deployment baseline expired for Issue " + issue + ". Reconcile any original queued/running execution and unresolved merge/installation effects. If the original execution and its bound installation are completed, inspect its independent acceptance and historical settlement projection, then use complete to settle that original responsibility; later maintenance does not require another approval or installation. Only genuinely new execution or installation needs a current versioned candidate and its actual authorization. A deferred delivery remains deferred until an actual owner resume source; do not open another confirmation just because validation or provider facts changed. Historical settlement never restores old execution permission. Do not revive a terminal task or replay an uncertain merge/installation.", CreatedAt: x.Now.UnixMilli()}); err != nil {
		return err
	}
	if err := tx.BindMessage(ctx, message, "work_item", issue, "delivery_candidate_expired"); err != nil {
		return err
	}
	return appendBackgroundSource(ctx, x, tx, "work_item:"+issue, issue, message, event, "issue.delivery_candidate_expired", nil)
}

// Context readers can address only the changed duty without loading every
// candidate when another object in the Issue changes.
func contextDeliveryEventDetails(out *Outcome, sourceTask string) {
	for i := range out.Events {
		e := &out.Events[i]
		if e.Kind == "issue.delivery_updated" {
			if e.Body == nil {
				e.Body = map[string]any{}
			}
			e.Body["source_task_id"] = sourceTask
		}
	}
}

// deliveryDutyContextEvent is emitted only by actual duty writers. It neither
// changes the Issue definition nor authorizes its next execution.
func deliveryDutyContextEvent(issue string, d protocol.IssueDeliveryDuty) Event {
	return Event{ObjectType: "issue", ObjectID: issue, Kind: "issue.delivery_updated", Body: map[string]any{"source_task_id": d.TaskID, "duty_revision": d.Revision, "duty_status": d.Status}}
}
