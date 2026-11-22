package protocol

const (
	OpDeliveriesPoll                    = "runner.deliveries_poll"
	OpDeliveriesReport                  = "runner.deliveries_report"
	OpRefreshDelivery                   = "work_items.refresh_delivery"
	CapabilityDeliveryTargetLineage     = "delivery-target-lineage-v1"
	CapabilityDeliveryPRWaitEvidence    = "delivery-pr-wait-evidence-v1"
	CapabilityDeliveryCandidateIdentity = "delivery-candidate-identity-v1"
	CapabilityDeliveryGitHub            = "delivery-observer-github-v1"
	CapabilityDeliveryCodebase          = "delivery-observer-codebase-v1"
)

type DeliveryActor struct {
	ID    string `json:"id,omitempty"`
	Login string `json:"login,omitempty"`
}

// DeliverySnapshot contains provider facts; missing identities remain unknown.
type DeliverySnapshot struct {
	Draft               *bool          `json:"draft,omitempty"`
	LineageErrorKind    string         `json:"lineage_error_kind,omitempty"`
	Mergeability        string         `json:"mergeability,omitempty"`
	CI                  *DeliveryCI    `json:"ci,omitempty"`
	TargetRef           string         `json:"target_ref,omitempty"`
	TargetSHA           string         `json:"target_sha,omitempty"`
	TargetContainsMerge bool           `json:"target_contains_merge,omitempty"`
	BaseSHA             string         `json:"base_sha,omitempty"`
	State               string         `json:"state"`
	HeadSHA             string         `json:"head_sha,omitempty"`
	MergeSHA            string         `json:"merge_sha,omitempty"`
	URL                 string         `json:"url,omitempty"`
	ProviderUpdatedAt   string         `json:"provider_updated_at,omitempty"`
	MergedAt            string         `json:"merged_at,omitempty"`
	ClosedAt            string         `json:"closed_at,omitempty"`
	MergedBy            *DeliveryActor `json:"merged_by,omitempty"`
	ClosedBy            *DeliveryActor `json:"closed_by,omitempty"`
}

// DeliveryCI is tied to the immutable commit actually inspected. Unknown,
// skipped and truncated checks never imply success for a different candidate.
type DeliveryCI struct {
	// AttemptChecks contains proof requested for an explicit workflow attempt.
	// Checks remains the provider's current commit rollup.
	AttemptChecks []DeliveryCheck `json:"attempt_checks,omitempty"`
	ErrorKind     string          `json:"error_kind,omitempty"`
	HeadSHA       string          `json:"head_sha"`
	State         string          `json:"state"`
	Checks        []DeliveryCheck `json:"checks"`
	Truncated     bool            `json:"truncated,omitempty"`
}

type DeliveryCheck struct {
	// Required is set only when the provider reports the check's status in
	// the observed PR's merge requirements.
	Required      *bool  `json:"required,omitempty"`
	ID            string `json:"id,omitempty"`
	Source        string `json:"source,omitempty"`
	App           string `json:"app,omitempty"`
	WorkflowRunID int64  `json:"workflow_run_id,omitempty"`
	Attempt       int    `json:"attempt,omitempty"`
	Name          string `json:"name"`
	State         string `json:"state"`
	Conclusion    string `json:"conclusion,omitempty"`
	URL           string `json:"url,omitempty"`
}

// RequiredAndPending reports positive evidence for a required check still in flight.
func (c DeliveryCheck) RequiredAndPending() bool {
	if c.Required == nil || !*c.Required {
		return false
	}
	switch c.State {
	case "expected", "pending", "queued", "in_progress", "waiting", "requested", "running":
		return true
	default:
		return false
	}
}

// DeliveryConflict is the first observation of a conflict for this candidate
// and baseline, retained when the provider subsequently reports a new head.
type DeliveryConflict struct {
	HeadSHA   string `json:"head_sha"`
	BaseSHA   string `json:"base_sha,omitempty"`
	TargetSHA string `json:"target_sha,omitempty"`
	CheckedAt int64  `json:"checked_at"`
	Observer  string `json:"observer"`
	Revision  int64  `json:"revision"`
}

func (c DeliveryConflict) Matches(o DeliveryObservation) bool {
	return c.HeadSHA == o.HeadSHA && c.BaseSHA == o.BaseSHA && c.TargetSHA == o.TargetSHA
}

// DeliveryObservation is independent of the work item's planning revision.
type DeliveryObservation struct {
	DeliverySnapshot
	PreviousHeadSHA         string            `json:"previous_head_sha,omitempty"`
	LastConflict            *DeliveryConflict `json:"last_conflict,omitempty"`
	Observer                string            `json:"observer,omitempty"`
	RetryCount              int               `json:"retry_count,omitempty"`
	RetryAt                 int64             `json:"retry_at,omitempty"`
	ErrorMessage            string            `json:"error_message,omitempty"`
	BaselineUpgradeID       string            `json:"baseline_upgrade_id,omitempty"`
	BaselineUpgradeRevision int64             `json:"baseline_upgrade_revision,omitempty"`
	WorkItemID              string            `json:"work_item_id"`
	DeliveryID              string            `json:"delivery_id"`
	Generation              int64             `json:"generation"`
	CheckedAt               int64             `json:"checked_at,omitempty"`
	AttemptedAt             int64             `json:"attempted_at,omitempty"`
	ErrorKind               string            `json:"error_kind,omitempty"`
	RefreshPending          bool              `json:"refresh_pending,omitempty"`
}

// CurrentConflict returns the retained conflict only while it applies to the
// PR observation being projected.
func (o DeliveryObservation) CurrentConflict() *DeliveryConflict {
	if c := o.LastConflict; c != nil && c.Matches(o) && o.State == "open" && o.Mergeability == "conflicting" {
		return c
	}
	return nil
}

type DeliveryPollPayload struct {
	InstanceID string `json:"instance_id"`
}
type DeliveryAssignment struct {
	PRWaitEvidence    bool             `json:"pr_wait_evidence,omitempty"`
	PRChecks          bool             `json:"pr_checks,omitempty"`
	CheckAttempts     []PRCheckAttempt `json:"check_attempts,omitempty"`
	TargetLineage     bool             `json:"target_lineage,omitempty"`
	CandidateIdentity bool             `json:"candidate_identity,omitempty"`
	WorkItemID        string           `json:"work_item_id"`
	Delivery          IssueDelivery    `json:"delivery"`
	Generation        int64            `json:"generation"`
	Nonce             string           `json:"nonce"`
}

// PRCheckAttempt limits provider enrichment to a live, explicit subscription.
type PRCheckAttempt struct {
	HeadSHA       string `json:"head_sha"`
	Name          string `json:"name"`
	WorkflowRunID int64  `json:"workflow_run_id"`
	Attempt       int    `json:"attempt"`
}

type DeliveryPollResult struct {
	Assignments []DeliveryAssignment `json:"assignments"`
}
type DeliveryReportPayload struct {
	InstanceID string            `json:"instance_id"`
	WorkItemID string            `json:"work_item_id"`
	DeliveryID string            `json:"delivery_id"`
	Generation int64             `json:"generation"`
	Nonce      string            `json:"nonce"`
	Snapshot   *DeliverySnapshot `json:"snapshot,omitempty"`
	ErrorKind  string            `json:"error_kind,omitempty"`
}
type DeliveryReportResult struct {
	Accepted bool `json:"accepted"`
}
type RefreshDeliveryPayload struct {
	DeliveryID string `json:"delivery_id"`
}
