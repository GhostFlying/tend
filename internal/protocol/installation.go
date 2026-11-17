package protocol

// InstallationReadiness is a local process/storage fact. It grants no business
// operation and says nothing about outstanding native or delivery receipts.
type InstallationReadiness struct {
	SupersededUpgradeID string         `json:"superseded_upgrade_id,omitempty"`
	Binding             UpgradeBinding `json:"binding"`
	Ready               bool           `json:"ready"`
	Protocol            int64          `json:"protocol"`
	StorageVersion      int64          `json:"storage_version"`
}
