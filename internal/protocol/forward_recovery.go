package protocol

// RecoveryIdentity pins the kernel identity as well as the service launch.
type RecoveryIdentity struct {
	PID        int    `json:"pid"`
	StartTicks uint64 `json:"start_ticks"`
	LaunchID   string `json:"launch_id"`
	SHA256     string `json:"sha256"`
}

// ForwardRecoveryPlan is a local maintenance authorization, never an upgrade
// start or a renewal of an old server grant. Its file stays private to the
// service operator. ResourceEvidence is the operator's reviewed handoff receipt;
// it is not inferred from a task being paused or a manager being idle.
type ForwardRecoveryPlan struct {
	LinkRequestID      string           `json:"link_request_id"`
	RecoveryID         string           `json:"recovery_id"`
	RequestID          string           `json:"request_id"`
	OriginalRequestID  string           `json:"original_request_id"`
	Original           UpgradeBinding   `json:"original"`
	OriginalProcess    RecoveryIdentity `json:"original_process"`
	OriginalLedger     UpgradeView      `json:"original_ledger"`
	LedgerRevision     int64            `json:"ledger_revision"`
	StateSHA256        string           `json:"state_sha256"`
	ConfigSHA256       string           `json:"config_sha256"`
	ExpectedGeneration uint64           `json:"expected_generation"`
	ReleaseDir         string           `json:"release_dir"`
	Successor          UpgradeBinding   `json:"successor"`
	OperatorUID        int              `json:"operator_uid"`
	WindowClaim        string           `json:"window_claim"`
	ResourceEvidence   string           `json:"resource_evidence"`
	ResourceSHA256     string           `json:"resource_sha256"`
	ExpiresAtMillis    int64            `json:"expires_at_millis"`
}

// RecoveryHealth is separate from the normal upgrade health contract. Epoch
// and sequence identify observations from one server boot, not wall-clock time.
type RecoveryHealth struct {
	RecoveryID string           `json:"recovery_id"`
	Binding    UpgradeBinding   `json:"binding"`
	Process    RecoveryIdentity `json:"process"`
	Epoch      string           `json:"epoch"`
	Sequence   uint64           `json:"sequence"`
	Protocol   int64            `json:"protocol"`
	Schema     int64            `json:"schema"`
	Registered bool             `json:"registered"`
	Fenced     bool             `json:"fenced"`
	Ready      bool             `json:"ready"`
	Failures   []string         `json:"failures"`
}

// RecoveryReceipt explicitly identifies forward success. It must never be
// converted into an UpdaterReceipt for the original upgrade binding.
type RecoveryReceipt struct {
	RecoveryID     string           `json:"recovery_id"`
	RequestID      string           `json:"request_id"`
	Original       UpgradeBinding   `json:"original"`
	Successor      UpgradeBinding   `json:"successor"`
	Process        RecoveryIdentity `json:"process"`
	Generation     uint64           `json:"generation"`
	Epoch          string           `json:"epoch"`
	FirstSequence  uint64           `json:"first_sequence"`
	SecondSequence uint64           `json:"second_sequence"`
}
