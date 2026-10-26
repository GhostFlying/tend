package protocol

// UpgradeEvidence freezes verified batch facts, not a product acceptance.
type UpgradeEvidence struct {
	UpgradeID      string                `json:"upgrade_id"`
	Revision       int64                 `json:"revision"`
	ReleaseID      string                `json:"release_id"`
	ManifestSHA256 string                `json:"manifest_sha256"`
	ServerSHA256   string                `json:"server_sha256"`
	CheckedAt      int64                 `json:"checked_at"`
	CompletedAt    int64                 `json:"completed_at"`
	Hosts          []UpgradeEvidenceHost `json:"hosts"`
}
type UpgradeEvidenceHost struct {
	HostID         string          `json:"host_id"`
	Status         string          `json:"status"`
	ArtifactSHA256 string          `json:"artifact_sha256"`
	PreviousSHA256 string          `json:"previous_sha256"`
	Receipt        *UpdaterReceipt `json:"receipt,omitempty"`
}
