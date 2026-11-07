package protocol

import "fmt"

func DeliveryCandidateQuestion(c IssueDeliveryCandidate) string {
	mergeScope := "merge authorized on approve"
	if c.ProviderState == "merged" {
		mergeScope = "existing merge " + c.MergeSHA + "; deployment only from current target " + c.TargetSHA + "; never merge again"
	}
	if c.Reconciliation != nil {
		mergeScope = fmt.Sprintf("verification only: existing merge %s; accepted evidence task %s; completed upgrade %s; installed source %s; no merge/deploy mutations authorized", c.MergeSHA, c.Reconciliation.EvidenceTaskID, c.Reconciliation.UpgradeID, c.Reconciliation.SourceCommit)
	}
	return fmt.Sprintf("Approve this delivery candidate and scope? PR %s; head %s; base %s; target %s; provider %s; validation %s; %s; deploy=%t; deployment scope: %s. Defer retains responsibility without merge/deploy; end_delivery explicitly ends remaining delivery responsibility without integration. A changed candidate needs a new question.", c.URL, c.HeadSHA, c.BaseSHA, c.TargetSHA, c.ProviderState, c.Validation, mergeScope, c.Deploy, c.DeploymentScope) + deliveryDeploymentQuestion(c)
}
func deliveryDeploymentQuestion(c IssueDeliveryCandidate) string {
	if c.Deployment == nil {
		return ""
	}
	if c.Reconciliation != nil {
		return fmt.Sprintf(" Verify existing installation only: server instance %s; complete host set %v; read back the referenced completed upgrade and original Issue before independent acceptance; no build, merge or installation is authorized. Baseline upgrade %s revision %d.", c.Deployment.ServerInstance, c.Deployment.HostIDs, c.Deployment.BaselineUpgradeID, c.Deployment.BaselineUpgradeRevision)
	}
	park := ""
	if c.Deployment.ParkIdleManagers {
		park = " This scope explicitly permits parking idle managers on those hosts through confirmed native close, retaining their logical responsibility for new runs after the window. Busy or unknown operations remain blockers; no business task cancellation or replay is authorized."
	}
	return park + fmt.Sprintf(" Tend executes the controlled self-update itself: server instance %s; complete host set %v; build from the verified merge and read back installed server/hosts and this original Issue before independent acceptance.", c.Deployment.ServerInstance, c.Deployment.HostIDs) + fmt.Sprintf(" Baseline upgrade %s revision %d.", c.Deployment.BaselineUpgradeID, c.Deployment.BaselineUpgradeRevision)
}
