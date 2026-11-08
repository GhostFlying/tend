package protocol

// CapabilityUpdaterSelfUpdate selects the optional supervisor-first extension.
// Unnegotiated peers receive neither targets nor observations of this type.
const CapabilityUpdaterSelfUpdate = "updater-self-update-v1"

type UpdaterSelfTarget struct {
	Authorized      bool   `json:"authorized,omitempty"`
	RequestID       string `json:"request_id"`
	ExpiresAtMillis int64  `json:"expires_at_millis"`
}

type UpdaterSelfReceipt struct {
	RequestID         string           `json:"request_id"`
	UpgradeID         string           `json:"upgrade_id"`
	ReleaseSetSHA256  string           `json:"release_set_sha256"`
	InstanceID        string           `json:"instance_id"`
	Previous          RecoveryIdentity `json:"previous"`
	Installed         RecoveryIdentity `json:"installed"`
	RetainedProcess   RecoveryIdentity `json:"retained_process"`
	CompletedAtMillis int64            `json:"completed_at_millis"`
}

// UpdaterSelfRejection proves preparation ended before any updater stop or replacement.
// A lost or uncertain dispatch never produces this receipt.
type UpdaterSelfRejection struct {
	RequestID        string `json:"request_id"`
	UpgradeID        string `json:"upgrade_id"`
	ReleaseSetSHA256 string `json:"release_set_sha256"`
	InstanceID       string `json:"instance_id"`
	RejectedAtMillis int64  `json:"rejected_at_millis"`
}

// A historical receipt and a fresh observation are separate evidence. Restarting
// the supervisor does not rewrite the original handoff's process identity.
type UpdaterSelfObservation struct {
	Rejection         *UpdaterSelfRejection `json:"rejection,omitempty"`
	InstanceID        string                `json:"instance_id"`
	Process           RecoveryIdentity      `json:"process"`
	SupervisedProcess RecoveryIdentity      `json:"supervised_process"`
	Receipt           *UpdaterSelfReceipt   `json:"receipt,omitempty"`
}
