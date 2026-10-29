package protocol

// ArchiveControlRequest retains one local action's request identity and CAS.
type ArchiveControlRequest struct {
	RequestID               string `json:"request_id"`
	ExpectedControlRevision int64  `json:"expected_control_revision"`
}

// ArchivePreparation is durable admission control, not proof of writer exit.
type ArchivePreparation struct {
	Revision         int64 `json:"revision"`
	Prepared         bool  `json:"prepared"`
	PreparedAtMillis int64 `json:"prepared_at_ms"`
	UpdatedAtMillis  int64 `json:"updated_at_ms"`
}

type ArchiveHostStatus struct {
	HostID   string         `json:"host_id"`
	Control  RunnerControl  `json:"control"`
	Runtime  *RunnerRuntime `json:"runtime,omitempty"`
	Fresh    bool           `json:"fresh"`
	OpenRuns int64          `json:"open_runs"`
	Blockers []string       `json:"blockers"`
}

// ArchiveOnlineStatus deliberately cannot claim that the current writer exited.
type ArchiveOnlineStatus struct {
	Preparation         ArchivePreparation  `json:"preparation"`
	ActiveOwnerRequests int64               `json:"active_owner_requests"`
	HostedDrained       bool                `json:"hosted_drained"`
	Hosts               []ArchiveHostStatus `json:"hosts"`
	Blockers            []string            `json:"blockers"`
	OnlineReady         bool                `json:"online_ready"`
}

// ArchiveHostControlRequest is an explicit application of existing host CAS.
type ArchiveHostControlRequest struct {
	RequestID               string `json:"request_id"`
	ExpectedControlRevision int64  `json:"expected_control_revision"`
	HostID                  string `json:"host_id"`
	Mode                    string `json:"mode"`
}
