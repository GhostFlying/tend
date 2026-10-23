package protocol

import "time"

// UpgradeHostView records one host's progress in a serialized release batch.
type UpgradeHostView struct {
	HostID          string `json:"host_id"`
	Status          string `json:"status"`
	Phase           string `json:"phase"`
	RemainingRuns   int64  `json:"remaining_runs,omitempty"`
	CurrentVersion  string `json:"current_version,omitempty"`
	TargetVersion   string `json:"target_version,omitempty"`
	ProtocolVersion int64  `json:"protocol_version"`
	DownloadedBytes int64  `json:"downloaded_bytes,omitempty"`
	TotalBytes      int64  `json:"total_bytes,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

// UpgradeView remains visible after completion until the next batch starts.
type UpgradeView struct {
	UpgradeID string            `json:"upgrade_id"`
	ReleaseID string            `json:"release_id"`
	Revision  int64             `json:"revision"`
	Status    string            `json:"status"`
	Phase     string            `json:"phase"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	Reason    string            `json:"reason,omitempty"`
	Hosts     []UpgradeHostView `json:"hosts"`
}

// RunnerUpgradeTarget names an authenticated immutable release download.
// URLs are relative to the configured server; runners never follow a new origin.
type RunnerUpgradeTarget struct {
	ManifestPath     string `json:"manifest_path"`
	ManifestSHA256   string `json:"manifest_sha256"`
	ManifestSize     int64  `json:"manifest_size"`
	ArtifactBasePath string `json:"artifact_base_path"`
}
