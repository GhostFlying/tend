package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type deliveryExecutionSnapshot struct {
	protocol.IssueDeliveryCandidate
	AssignmentEpoch int64 `json:"assignment_epoch"`
}

func deliveryBaseline(ctx context.Context, q store.Querier) (store.UpgradeRow, error) {
	r, err := store.LatestUpgrade(ctx, q)
	if errors.Is(err, store.ErrNotFound) {
		return store.UpgradeRow{}, nil
	}
	return r, err
}

func normalizeDeliveryBaseline(ctx context.Context, q store.Querier, c *protocol.IssueDeliveryCandidate) error {
	if !c.Deploy || c.Deployment == nil {
		return nil
	}
	r, err := deliveryBaseline(ctx, q)
	if err != nil {
		return err
	}
	if r.View.Status == "running" || r.View.Status == "attention" {
		return protocol.Errorf(protocol.CodeUnknownOutcome, "the controlled upgrade target must settle before confirming another deployment")
	}
	target := c.Deployment
	if target.BaselineUpgradeID == "" && target.BaselineUpgradeRevision == 0 {
		target.BaselineUpgradeID = r.View.UpgradeID
		target.BaselineUpgradeRevision = r.View.Revision
	}
	return checkDeliveryBaseline(ctx, q, *c)
}

func checkDeliveryBaseline(ctx context.Context, q store.Querier, c protocol.IssueDeliveryCandidate) error {
	if !c.Deploy || c.Deployment == nil {
		return nil
	}
	r, err := deliveryBaseline(ctx, q)
	if err != nil {
		return err
	}
	if r.View.Status == "running" || r.View.Status == "attention" {
		return protocol.Errorf(protocol.CodeUnknownOutcome, "the controlled upgrade target is still running or requires reconciliation")
	}
	if c.Deployment.BaselineUpgradeID != r.View.UpgradeID || c.Deployment.BaselineUpgradeRevision != r.View.Revision {
		return protocol.Errorf(protocol.CodeStaleRevision, "deployment baseline changed; refresh provider facts and confirm a current candidate")
	}
	return nil
}

// Only one started delivery can own this service's fixed updater target. A
// pending second action has no run/lease/environment. The first retains logical
// ownership across question handoff, installation and independent acceptance.
func deliveryTargetAvailable(ctx context.Context, q store.Querier, task string) (bool, error) {
	r, err := deliveryBaseline(ctx, q)
	if err != nil {
		return false, err
	}
	if r.View.Status == "running" || r.View.Status == "attention" {
		return false, nil
	}
	var held bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_delivery_executions e JOIN tasks t ON t.task_id=e.task_id
 WHERE e.task_id<>? AND json_extract(e.candidate_json,'$.deploy')=1 AND EXISTS(SELECT 1 FROM runs r WHERE r.task_id=e.task_id)
 AND (t.lifecycle NOT IN ('completed','failed','cancelled') OR EXISTS(SELECT 1 FROM runs r WHERE r.task_id=e.task_id AND r.observed_state IN ('starting','running','waiting','interrupted','unknown'))))`, task).Scan(&held)
	return !held, err
}

func deliveryObservationBaseline(ctx context.Context, q store.Querier, issue string, c protocol.IssueDeliveryCandidate) error {
	if !c.Deploy || c.Deployment == nil {
		return nil
	}
	if err := checkDeliveryBaseline(ctx, q, c); err != nil {
		return err
	}
	o, err := store.GetDeliveryObservation(ctx, q, issue, c.DeliveryID)
	if err != nil {
		return err
	}
	if o.Observation.BaselineUpgradeID != c.Deployment.BaselineUpgradeID || o.Observation.BaselineUpgradeRevision != c.Deployment.BaselineUpgradeRevision {
		return protocol.Errorf(protocol.CodeStaleRevision, "provider query predates this deployment baseline; refresh it before confirmation")
	}
	return nil
}

// checkDeliveryUpgradeStart is used only by the local verified-bundle boundary.
// It does not trust the worker's prose or treat a resolved technical question as
// user permission. The slot, owner epoch, exact answer and source are rechecked.
func checkDeliveryUpgradeStart(ctx context.Context, x *Exec, q store.Querier, c StartUpgrade) (*protocol.IssueDeliveryCandidate, error) {
	if c.DeliveryTaskID == "" {
		return nil, nil
	}
	var raw, issue, source, bound string
	var revision int64
	if err := q.QueryRowContext(ctx, `SELECT candidate_json,issue_id,source_task_id,duty_revision,upgrade_id FROM issue_delivery_executions WHERE task_id=?`, c.DeliveryTaskID).Scan(&raw, &issue, &source, &revision, &bound); err != nil {
		return nil, errNotFound(err, "approved delivery execution does not exist")
	}
	var frozen deliveryExecutionSnapshot
	if err := json.Unmarshal([]byte(raw), &frozen); err != nil {
		return nil, err
	}
	if !frozen.Deploy || frozen.Reconciliation != nil || frozen.Deployment == nil {
		return nil, protocol.Errorf(protocol.CodeForbidden, "this execution does not authorize a new controlled installation")
	}
	if bound != "" {
		return nil, protocol.Errorf(protocol.CodeInvalidTransition, "execution already started an upgrade; reconcile its original batch")
	}
	current, err := store.GetCanonicalIssue(ctx, q, issue)
	if err != nil {
		return nil, err
	}
	if current.AssignmentEpoch != frozen.AssignmentEpoch || current.State != protocol.IssueActive || current.AssignmentState != "active" {
		return nil, protocol.Errorf(protocol.CodeStaleRevision, "delivery responsibility changed")
	}
	for parent := current.ParentIssueID; parent != ""; {
		ancestor, err := store.GetCanonicalIssue(ctx, q, parent)
		if err != nil {
			return nil, err
		}
		if ancestor.State != protocol.IssueActive || ancestor.AssignmentState != "active" {
			return nil, protocol.Errorf(protocol.CodeForbidden, "an ancestor paused or ended this deployment responsibility")
		}
		parent = ancestor.ParentIssueID
	}
	d, err := store.GetIssueDeliveryDuty(ctx, q, issue, source)
	if err != nil {
		return nil, err
	}
	if d.ExecutionTaskID != c.DeliveryTaskID || d.Revision != revision || d.Status != "executing" {
		return nil, protocol.Errorf(protocol.CodeStaleRevision, "delivery execution was superseded")
	}
	option, err := deliveryOwnerOption(ctx, q, d)
	if err != nil {
		return nil, err
	}
	if option != "approve" {
		return nil, protocol.Errorf(protocol.CodeForbidden, "an exact current owner answer is required")
	}
	task, err := store.GetTask(ctx, q, c.DeliveryTaskID)
	if err != nil {
		return nil, err
	}
	var running bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r JOIN leases l USING(run_id) WHERE r.task_id=? AND r.role='implement' AND r.observed_state='running' AND r.desired_state='run' AND l.status='active' AND l.expires_at>?)`, task.TaskID, x.Now.UnixMilli()).Scan(&running); err != nil {
		return nil, err
	}
	if task.ControlMode != protocol.ControlManaged || !running {
		return nil, protocol.Errorf(protocol.CodeForbidden, "a current running delivery lease is required")
	}
	available, err := deliveryTargetAvailable(ctx, q, task.TaskID)
	if err != nil {
		return nil, err
	}
	if !available {
		return nil, protocol.Errorf(protocol.CodeUnknownOutcome, "another delivery still owns the controlled upgrade target")
	}
	if err := deliveryObservationBaseline(ctx, q, issue, frozen.IssueDeliveryCandidate); err != nil {
		return nil, err
	}
	observation, err := store.GetDeliveryObservation(ctx, q, issue, frozen.DeliveryID)
	if err != nil {
		return nil, err
	}
	target, err := deliveryDeploymentSource(frozen.IssueDeliveryCandidate, observation.Observation)
	if err != nil {
		return nil, err
	}
	sourceBuild, ok := c.ArtifactSources[c.ServerHash]
	if !ok || sourceBuild.Modified || !validDeliverySHA(sourceBuild.Commit) || sourceBuild.Commit != target {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "server artifact must be an unmodified build of the approved current source")
	}
	if c.ServerInstance != frozen.Deployment.ServerInstance {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "controlled server is outside the approved scope")
	}
	return &frozen.IssueDeliveryCandidate, nil
}

// Refreshes are passive observations. They fence reads started against a prior
// deployment baseline without allocating a model turn or execution slot.
func refreshDeliveryBaselineQueries(ctx context.Context, x *Exec, tx *store.Tx) error {
	rows, err := tx.Q().QueryContext(ctx, `SELECT issue_id,json_extract(body_json,'$.candidate.delivery_id') FROM issue_delivery_duties WHERE status IN ('preparing','awaiting_owner','deferred','executing') AND json_extract(body_json,'$.candidate.deploy')=1`)
	if err != nil {
		return err
	}
	type ref struct{ issue, delivery string }
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.issue, &r.delivery); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, r := range refs {
		o, err := store.GetDeliveryObservation(ctx, tx.Q(), r.issue, r.delivery)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		o.Observation.Generation++
		o.Nonce = ""
		o.QueryUntil = 0
		o.NextCheckAt = x.Now.UnixMilli()
		o.RefreshRequested = true
		o.Observation.RefreshPending = true
		if err := tx.PutDeliveryObservation(ctx, o); err != nil {
			return err
		}
	}
	return nil
}

// BaseSHA is the PR base. Installation always uses the independently observed
// target, proven to contain the merge, never that historical base commit.
func deliveryDeploymentSource(candidate protocol.IssueDeliveryCandidate, o protocol.DeliveryObservation) (string, error) {
	if o.ErrorKind != "" || o.RefreshPending || o.State != "merged" || o.HeadSHA != candidate.HeadSHA || o.MergeSHA == "" {
		return "", protocol.Errorf(protocol.CodeStaleRevision, "verify the approved head's merge before installation")
	}
	target := o.MergeSHA
	if candidate.ProviderState == "merged" {
		if o.MergeSHA != candidate.MergeSHA || o.BaseSHA != candidate.BaseSHA || o.TargetSHA != candidate.TargetSHA || !o.TargetContainsMerge {
			return "", protocol.Errorf(protocol.CodeStaleRevision, "merged deployment source changed")
		}
		target = candidate.TargetSHA
	} else if o.TargetSHA != o.MergeSHA || !o.TargetContainsMerge {
		return "", protocol.Errorf(protocol.CodeStaleRevision, "main advanced beyond this merge; confirm a current deployment source")
	}
	return target, nil
}
