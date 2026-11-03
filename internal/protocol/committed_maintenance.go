package protocol

// CommittedMaintenancePlan authorizes a new local server maintenance window.
// It never impersonates an unfinished upgrade or changes business run identity.
type MaintenanceHostControl struct {
	HostID           string `json:"host_id"`
	Revision         int64  `json:"revision"`
	PreviousMode     string `json:"previous_mode"`
	RequestID        string `json:"request_id"`
	RestoreRequestID string `json:"restore_request_id"`
}

type CommittedMaintenancePlan struct {
	ParentReceipt              *CommittedMaintenanceReceipt `json:"parent_receipt,omitempty"`
	ParentMaintenanceID        string                       `json:"parent_maintenance_id,omitempty"`
	ParentPlanSHA256           string                       `json:"parent_plan_sha256,omitempty"`
	ParentArchiveSHA256        string                       `json:"parent_archive_sha256,omitempty"`
	ParentGeneration           uint64                       `json:"parent_generation,omitempty"`
	ParentPlan                 *CommittedMaintenancePlan    `json:"parent_plan,omitempty"`
	FailedProcessExited        bool                         `json:"failed_process_exited,omitempty"`
	PreparationRequestID       string                       `json:"preparation_request_id"`
	PreparationCancelRequestID string                       `json:"preparation_cancel_request_id"`
	HostControls               []MaintenanceHostControl     `json:"host_controls"`
	MaintenanceID              string                       `json:"maintenance_id"`
	RequestID                  string                       `json:"request_id"`
	LinkRequestID              string                       `json:"link_request_id"`
	Previous                   UpgradeBinding               `json:"previous"`
	PreviousProcess            RecoveryIdentity             `json:"previous_process"`
	ExpectedGeneration         uint64                       `json:"expected_generation"`
	StateSHA256                string                       `json:"state_sha256"`
	ConfigSHA256               string                       `json:"config_sha256"`
	ReleaseDir                 string                       `json:"release_dir"`
	Successor                  UpgradeBinding               `json:"successor"`
	OperatorUID                int                          `json:"operator_uid"`
	WindowClaim                string                       `json:"window_claim"`
	ResourceEvidence           string                       `json:"resource_evidence"`
	ResourceSHA256             string                       `json:"resource_sha256"`
	ExpiresAtMillis            int64                        `json:"expires_at_millis"`
	PreparationRevision        int64                        `json:"preparation_revision"`
}

// CommittedMaintenanceReceipt proves this window's independent successor.
// The previous upgrade and forward-recovery receipts remain historical facts.
type CommittedMaintenanceReceipt struct {
	MaintenanceID  string           `json:"maintenance_id"`
	RequestID      string           `json:"request_id"`
	Previous       UpgradeBinding   `json:"previous"`
	Successor      UpgradeBinding   `json:"successor"`
	Process        RecoveryIdentity `json:"process"`
	Generation     uint64           `json:"generation"`
	Epoch          string           `json:"epoch"`
	FirstSequence  uint64           `json:"first_sequence"`
	SecondSequence uint64           `json:"second_sequence"`
}

type CommittedMaintenanceLedger struct {
	Plan    CommittedMaintenancePlan     `json:"plan"`
	Process RecoveryIdentity             `json:"process"`
	Receipt *CommittedMaintenanceReceipt `json:"receipt,omitempty"`
	Linked  bool                         `json:"linked"`
}

type CommittedMaintenanceHealth struct {
	MaintenanceID string           `json:"maintenance_id"`
	Binding       UpgradeBinding   `json:"binding"`
	Process       RecoveryIdentity `json:"process"`
	Epoch         string           `json:"epoch"`
	Sequence      uint64           `json:"sequence"`
	Protocol      int64            `json:"protocol"`
	Schema        int64            `json:"schema"`
	Registered    bool             `json:"registered"`
	Fenced        bool             `json:"fenced"`
	Ready         bool             `json:"ready"`
	Failures      []string         `json:"failures"`
}

func CommittedHandoffActions() []string { return []string{"plan", "apply", "status"} }

// MaintenanceContinuation is a new bounded local authorization for the same
// immutable candidate after a definitely pre-launch interruption.
type MaintenanceContinuation struct {
	RequestID          string `json:"request_id"`
	ExpectedGeneration uint64 `json:"expected_generation"`
	WindowClaim        string `json:"window_claim"`
	ExpiresAtMillis    int64  `json:"expires_at_millis"`
	ResourceEvidence   string `json:"resource_evidence"`
	ResourceSHA256     string `json:"resource_sha256"`
}
