package protocol

import "time"

// UpgradeHostView records one host's progress in a serialized release batch.
type UpgradeHostView struct {
	Updater         *UpdaterSelfObservation `json:"updater,omitempty"`
	HostID          string                  `json:"host_id"`
	Status          string                  `json:"status"`
	Phase           string                  `json:"phase"`
	RemainingRuns   int64                   `json:"remaining_runs,omitempty"`
	CurrentVersion  string                  `json:"current_version,omitempty"`
	TargetVersion   string                  `json:"target_version,omitempty"`
	ProtocolVersion int64                   `json:"protocol_version"`
	DownloadedBytes int64                   `json:"downloaded_bytes,omitempty"`
	TotalBytes      int64                   `json:"total_bytes,omitempty"`
	Reason          string                  `json:"reason,omitempty"`
}

// UpgradeWindowStop pins an incompatible writer and the leases it owned at
// admission. Its process identity survives retries and lost replies.
type UpgradeWindowStop struct {
	Forced          bool             `json:"forced,omitempty"`
	HostID          string           `json:"host_id"`
	JournalID       string           `json:"journal_id"`
	Process         RecoveryIdentity `json:"process"`
	Protocol        int64            `json:"protocol"`
	Generation      int64            `json:"generation"`
	SourceUpgradeID string           `json:"source_upgrade_id,omitempty"`
	Primary         bool             `json:"primary,omitempty"`
	Runs            []LeaseRef       `json:"runs"`
	State           string           `json:"state"`
	UnknownRuns     []string         `json:"unknown_runs,omitempty"`
}

type UpgradeManagerParkView struct {
	TaskID string `json:"task_id"`
	RunID  string `json:"run_id"`
	Phase  string `json:"phase"`
	Reason string `json:"reason,omitempty"`
}

// UpgradeView remains visible after completion until the next batch starts.
type UpgradeView struct {
	ForceMaintenance      bool                     `json:"force_maintenance,omitempty"`
	WindowStops           []UpgradeWindowStop      `json:"window_stops,omitempty"`
	ServerOnly            bool                     `json:"server_only,omitempty"`
	ServerUpdater         *UpdaterSelfObservation  `json:"server_updater,omitempty"`
	OverallDeadlineMillis int64                    `json:"overall_deadline_millis,omitempty"`
	ManagerParks          []UpgradeManagerParkView `json:"manager_parks,omitempty"`
	SourceCommit          string                   `json:"source_commit,omitempty"`
	UpgradeID             string                   `json:"upgrade_id"`
	ReleaseID             string                   `json:"release_id"`
	Revision              int64                    `json:"revision"`
	Status                string                   `json:"status"`
	Phase                 string                   `json:"phase"`
	CreatedAt             time.Time                `json:"created_at"`
	UpdatedAt             time.Time                `json:"updated_at"`
	Reason                string                   `json:"reason,omitempty"`
	Hosts                 []UpgradeHostView        `json:"hosts"`
}

// RunnerUpgradeTarget names an authenticated immutable release download.
// URLs are relative to the configured server; runners never follow a new origin.
// RunnerConfiguration is metadata only; file contents stay on the target host.
type RunnerConfiguration struct {
	PreviousSHA256 string   `json:"previous_sha256"`
	SHA256         string   `json:"sha256"`
	Providers      []string `json:"providers"`
}

const CapabilityRunnerConfiguration = "runner-config-maintenance-v1"

const CapabilityCompleteReleaseSet = "complete-release-set-v1"

// ReleaseSetTarget is an authenticated, immutable metadata download.
type ReleaseSetTarget struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type RunnerUpgradeTarget struct {
	Rolling          bool                 `json:"rolling,omitempty"`
	PrepareOnly      bool                 `json:"prepare_only,omitempty"`
	Updater          *UpdaterSelfTarget   `json:"updater,omitempty"`
	ReleaseSet       *ReleaseSetTarget    `json:"release_set,omitempty"`
	Configuration    *RunnerConfiguration `json:"configuration,omitempty"`
	ManifestPath     string               `json:"manifest_path"`
	ManifestSHA256   string               `json:"manifest_sha256"`
	ManifestSize     int64                `json:"manifest_size"`
	ArtifactBasePath string               `json:"artifact_base_path"`
}

type RollingAuthorization struct {
	Revision int64                     `json:"revision"`
	Request  RollingRunnerGrantRequest `json:"request"`
}

type RollingTargetRequest struct {
	UpgradeID        string           `json:"upgrade_id"`
	ManifestSHA256   string           `json:"manifest_sha256"`
	ReleaseSetSHA256 string           `json:"release_set_sha256"`
	Predecessor      RecoveryIdentity `json:"predecessor"`
}
type RollingTarget struct {
	Revision int64             `json:"revision"`
	Updater  UpdaterSelfTarget `json:"updater"`
}

// RollingRunnerGrantRequest is a request to launch another process alongside
// the observed predecessor. It carries no authority to stop either process.
type RollingRunnerGrantRequest struct {
	Binding UpgradeBinding         `json:"binding"`
	Updater UpdaterSelfObservation `json:"updater"`
}

// RunnerRetirement binds the stop and its acknowledgement to one predecessor.
type RunnerRetirement struct {
	// Echoed only after the server offers bounded maintenance on this process.
	WindowID        string           `json:"window_id,omitempty"`
	Unused          bool             `json:"unused,omitempty"`
	UpgradeID       string           `json:"upgrade_id"`
	Process         RecoveryIdentity `json:"process"`
	AuthorizationID string           `json:"authorization_id,omitempty"`
}

type RunnerRetirementResult struct {
	RemainingMillis int64 `json:"remaining_ms,omitempty"`
	RunnerRetirement
	ReadySequence int64 `json:"ready_sequence,omitempty"`
	Allowed       bool  `json:"allowed"`
	Confirmed     bool  `json:"confirmed"`
}
