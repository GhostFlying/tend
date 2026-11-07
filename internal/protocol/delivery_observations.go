package protocol

const (
	OpDeliveriesPoll                    = "runner.deliveries_poll"
	OpDeliveriesReport                  = "runner.deliveries_report"
	OpRefreshDelivery                   = "work_items.refresh_delivery"
	CapabilityDeliveryTargetLineage     = "delivery-target-lineage-v1"
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
	LineageErrorKind    string         `json:"lineage_error_kind,omitempty"`
	Mergeability        string         `json:"mergeability,omitempty"`
	CI                  *DeliveryCI    `json:"ci,omitempty"`
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
	ErrorKind string          `json:"error_kind,omitempty"`
	HeadSHA   string          `json:"head_sha"`
	State     string          `json:"state"`
	Checks    []DeliveryCheck `json:"checks"`
	Truncated bool            `json:"truncated,omitempty"`
}

type DeliveryCheck struct {
	Name       string `json:"name"`
	State      string `json:"state"`
	Conclusion string `json:"conclusion,omitempty"`
	URL        string `json:"url,omitempty"`
}

// DeliveryObservation is independent of the work item's planning revision.
type DeliveryObservation struct {
	DeliverySnapshot
	Observer                string `json:"observer,omitempty"`
	RetryCount              int    `json:"retry_count,omitempty"`
	RetryAt                 int64  `json:"retry_at,omitempty"`
	ErrorMessage            string `json:"error_message,omitempty"`
	BaselineUpgradeID       string `json:"baseline_upgrade_id,omitempty"`
	BaselineUpgradeRevision int64  `json:"baseline_upgrade_revision,omitempty"`
	WorkItemID              string `json:"work_item_id"`
	DeliveryID              string `json:"delivery_id"`
	Generation              int64  `json:"generation"`
	CheckedAt               int64  `json:"checked_at,omitempty"`
	AttemptedAt             int64  `json:"attempted_at,omitempty"`
	ErrorKind               string `json:"error_kind,omitempty"`
	RefreshPending          bool   `json:"refresh_pending,omitempty"`
}

type DeliveryPollPayload struct {
	InstanceID string `json:"instance_id"`
}
type DeliveryAssignment struct {
	TargetLineage     bool          `json:"target_lineage,omitempty"`
	CandidateIdentity bool          `json:"candidate_identity,omitempty"`
	WorkItemID        string        `json:"work_item_id"`
	Delivery          IssueDelivery `json:"delivery"`
	Generation        int64         `json:"generation"`
	Nonce             string        `json:"nonce"`
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
