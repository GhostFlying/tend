package app

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// A fresh verification task may reconcile an actual legacy delivery. No old
// task is relabeled as typed execution, and this mode authorizes no mutation.
func checkDeliveryReconciliation(ctx context.Context, q store.Querier, issue string, c *protocol.IssueDeliveryCandidate) (protocol.IssueEvidence, int64, error) {
	r := c.Reconciliation
	if r == nil {
		return protocol.IssueEvidence{}, 0, nil
	}
	if !c.Deploy || c.Deployment == nil || c.ProviderState != "merged" || r.EvidenceTaskID == "" || r.UpgradeID == "" {
		return protocol.IssueEvidence{}, 0, protocol.Errorf(protocol.CodeInvalidRequest, "existing delivery requires a merged candidate, accepted evidence task and exact upgrade")
	}
	primary, err := store.GetTaskIssueOwner(ctx, q, r.EvidenceTaskID)
	if err != nil {
		return protocol.IssueEvidence{}, 0, err
	}
	task, spec, err := loadTask(ctx, q, r.EvidenceTaskID)
	if err != nil {
		return protocol.IssueEvidence{}, 0, err
	}
	if primary != issue || task.Lifecycle != protocol.TaskCompleted || task.Kind == protocol.TaskKindIssueManager || !spec.RequiresDelivery(domain.DeliveryReport) {
		return protocol.IssueEvidence{}, 0, protocol.Errorf(protocol.CodeInvalidTransition, "existing delivery evidence must be an accepted report under this original Issue")
	}
	raw, err := store.GetArtifactRef(ctx, q, task.CurrentArtifactID)
	if err != nil {
		return protocol.IssueEvidence{}, 0, err
	}
	var artifact protocol.ArtifactPayload
	if err := json.Unmarshal([]byte(raw), &artifact); err != nil {
		return protocol.IssueEvidence{}, 0, err
	}
	id, err := deliveryUpgradeReference(artifact.Report)
	if err != nil {
		return protocol.IssueEvidence{}, 0, err
	}
	if id != r.UpgradeID {
		return protocol.IssueEvidence{}, 0, protocol.Errorf(protocol.CodeInvalidRequest, "original accepted report does not reference this upgrade")
	}
	batch, err := deliveryUpgradeBinding(ctx, q, *c.Deployment, id, task.CreatedAt)
	if err != nil {
		return protocol.IssueEvidence{}, 0, err
	}
	if batch.View.Status != "completed" || batch.View.Phase != "completed" || !validDeliverySHA(batch.View.SourceCommit) {
		return protocol.IssueEvidence{}, 0, protocol.Errorf(protocol.CodeUnknownOutcome, "existing upgrade needs completed installations and verified source identity")
	}
	if r.SourceCommit == "" {
		r.SourceCommit = batch.View.SourceCommit
	}
	if r.SourceCommit != batch.View.SourceCommit {
		return protocol.IssueEvidence{}, 0, protocol.Errorf(protocol.CodeStaleRevision, "existing installation source changed")
	}
	evidence, err := completedTaskEvidence(ctx, q, task.TaskID, "Original completed deployment report")
	return evidence, task.CreatedAt, err
}

func deliveryUpgradeReference(report *protocol.ResultReport) (string, error) {
	if report == nil {
		return "", protocol.Errorf(protocol.CodeInvalidRequest, "deployment report is required")
	}
	id := ""
	count := 0
	for _, e := range report.Evidence {
		if strings.HasPrefix(e, "upgrade:") {
			count++
			id = strings.TrimPrefix(e, "upgrade:")
		}
	}
	if count != 1 || id == "" {
		return "", protocol.Errorf(protocol.CodeInvalidRequest, "report requires exactly one upgrade:<id> evidence reference")
	}
	return id, nil
}
