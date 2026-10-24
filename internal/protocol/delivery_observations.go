package protocol

const (
	OpDeliveriesPoll           = "runner.deliveries_poll"
	OpDeliveriesReport         = "runner.deliveries_report"
	OpRefreshDelivery          = "work_items.refresh_delivery"
	CapabilityDeliveryGitHub   = "delivery-observer-github-v1"
	CapabilityDeliveryCodebase = "delivery-observer-codebase-v1"
)

type DeliveryActor struct {
	ID    string `json:"id,omitempty"`
	Login string `json:"login,omitempty"`
}

// DeliverySnapshot contains provider facts; missing identities remain unknown.
type DeliverySnapshot struct {
	State             string         `json:"state"`
	HeadSHA           string         `json:"head_sha,omitempty"`
	MergeSHA          string         `json:"merge_sha,omitempty"`
	URL               string         `json:"url,omitempty"`
	ProviderUpdatedAt string         `json:"provider_updated_at,omitempty"`
	MergedAt          string         `json:"merged_at,omitempty"`
	ClosedAt          string         `json:"closed_at,omitempty"`
	MergedBy          *DeliveryActor `json:"merged_by,omitempty"`
	ClosedBy          *DeliveryActor `json:"closed_by,omitempty"`
}

// DeliveryObservation is independent of the work item's planning revision.
type DeliveryObservation struct {
	DeliverySnapshot
	WorkItemID     string `json:"work_item_id"`
	DeliveryID     string `json:"delivery_id"`
	Generation     int64  `json:"generation"`
	CheckedAt      int64  `json:"checked_at,omitempty"`
	AttemptedAt    int64  `json:"attempted_at,omitempty"`
	ErrorKind      string `json:"error_kind,omitempty"`
	RefreshPending bool   `json:"refresh_pending,omitempty"`
}

type DeliveryPollPayload struct {
	InstanceID string `json:"instance_id"`
}
type DeliveryAssignment struct {
	WorkItemID string           `json:"work_item_id"`
	Delivery   WorkItemDelivery `json:"delivery"`
	Generation int64            `json:"generation"`
	Nonce      string           `json:"nonce"`
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
