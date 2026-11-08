package protocol

// MaintenanceBackend pins an external native executor, never a quota policy.
type MaintenanceBackend struct {
	NetworkAccess  bool     `json:"network_access,omitempty"`
	WritableRoots  []string `json:"writable_roots,omitempty"`
	TUIURL         string   `json:"tui_url,omitempty"`
	Agent          string   `json:"agent"`
	Socket         string   `json:"socket"`
	Model          string   `json:"model,omitempty"`
	Effort         string   `json:"effort,omitempty"`
	ApprovalPolicy string   `json:"approval_policy"`
	Sandbox        string   `json:"sandbox"`
}

type MaintenanceCase struct {
	Observed    bool   `json:"observed"`
	ID          string `json:"id"`
	Source      string `json:"source"`
	Code        string `json:"code"`
	Evidence    string `json:"evidence"`
	Fingerprint string `json:"fingerprint"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	FirstSeen   int64  `json:"first_seen"`
	LastSeen    int64  `json:"last_seen"`
	DeferUntil  int64  `json:"defer_until,omitempty"`
}

// MaintenancePredecessor exists only after a confirmed interrupt switch.
type MaintenancePredecessor struct {
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
}

type MaintenanceSession struct {
	SandboxPolicy      map[string]any          `json:"sandbox_policy,omitempty"`
	InputReasons       []string                `json:"input_reasons,omitempty"`
	InterruptRequested bool                    `json:"interrupt_requested,omitempty"`
	ID                 string                  `json:"id"`
	Backend            MaintenanceBackend      `json:"backend"`
	SessionID          string                  `json:"session_id,omitempty"`
	RolloutPath        string                  `json:"rollout_path,omitempty"`
	Phase              string                  `json:"phase"`
	InputID            string                  `json:"input_id,omitempty"`
	TurnID             string                  `json:"turn_id,omitempty"`
	Prompt             string                  `json:"prompt,omitempty"`
	StartedAt          int64                   `json:"started_at"`
	EndedAt            int64                   `json:"ended_at,omitempty"`
	Predecessor        *MaintenancePredecessor `json:"predecessor,omitempty"`
}

type MaintenanceStatus struct {
	Review         *MaintenanceReview      `json:"review,omitempty"`
	NativeError    string                  `json:"native_error,omitempty"`
	OpenCases      int                     `json:"open_cases"`
	Upgrade        *MaintenanceUpgrade     `json:"upgrade,omitempty"`
	CheckRequested bool                    `json:"check_requested,omitempty"`
	Format         int                     `json:"format"`
	Revision       int64                   `json:"revision"`
	Paused         bool                    `json:"paused"`
	Backend        MaintenanceBackend      `json:"backend"`
	PendingBackend *MaintenanceBackend     `json:"pending_backend,omitempty"`
	SwitchMode     string                  `json:"switch_mode,omitempty"`
	Active         *MaintenanceSession     `json:"active,omitempty"`
	Predecessor    *MaintenancePredecessor `json:"predecessor,omitempty"`
	Pending        []string                `json:"pending"`
	Cases          []MaintenanceCase       `json:"cases"`
	History        []MaintenanceSession    `json:"history"`
	LastCheck      int64                   `json:"last_check,omitempty"`
	LastAudit      int64                   `json:"last_audit,omitempty"`
	LastError      string                  `json:"last_error,omitempty"`
	EventSeq       int64                   `json:"event_seq,omitempty"`
	HealthFailures int                     `json:"health_failures,omitempty"`
}

// MaintenanceOverview is optional read-only reporting, without native content.
type MaintenanceOverview struct {
	Configured   bool   `json:"configured"`
	Available    bool   `json:"available"`
	ObservedAt   int64  `json:"observed_at"`
	LastCheck    int64  `json:"last_check,omitempty"`
	LastAudit    int64  `json:"last_audit,omitempty"`
	Paused       bool   `json:"paused"`
	OpenCases    int    `json:"open_cases"`
	Pending      int    `json:"pending"`
	Agent        string `json:"agent,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	Phase        string `json:"phase,omitempty"`
	UpgradePhase string `json:"upgrade_phase,omitempty"`
	Error        string `json:"error,omitempty"`
}

type MaintenanceRequest struct {
	TurnID           string              `json:"turn_id,omitempty"`
	Base             string              `json:"base,omitempty"`
	Head             string              `json:"head,omitempty"`
	ReleaseDir       string              `json:"release_dir,omitempty"`
	UpgradeID        string              `json:"upgrade_id,omitempty"`
	RequestID        string              `json:"request_id"`
	ExpectedRevision int64               `json:"expected_revision"`
	Backend          *MaintenanceBackend `json:"backend,omitempty"`
	Mode             string              `json:"mode,omitempty"`
	CaseID           string              `json:"case_id,omitempty"`
	Reason           string              `json:"reason,omitempty"`
	Until            int64               `json:"until,omitempty"`
	SessionID        string              `json:"session_id,omitempty"`
}

type MaintenanceReceipt struct {
	Action    string              `json:"action,omitempty"`
	Request   *MaintenanceRequest `json:"request,omitempty"`
	RequestID string              `json:"request_id"`
	Revision  int64               `json:"revision"`
}

func MaintenanceActions() []string {
	return []string{"status", "cases", "show", "history", "session", "request", "check", "audit", "pause", "resume", "defer", "resolve", "config", "reconcile", "review", "upgrade-plan", "upgrade-apply", "upgrade-status"}
}

type MaintenanceReview struct {
	Outcome   string `json:"outcome,omitempty"`
	RequestID string `json:"request_id"`
	Base      string `json:"base"`
	Head      string `json:"head"`
	Phase     string `json:"phase"`
}

type MaintenanceUpgrade struct {
	ID             string          `json:"id"`
	ReleaseDir     string          `json:"release_dir"`
	ReleaseID      string          `json:"release_id"`
	ManifestSHA256 string          `json:"manifest_sha256"`
	ArtifactSHA256 string          `json:"artifact_sha256"`
	Phase          string          `json:"phase"`
	Binding        *UpgradeBinding `json:"binding,omitempty"`
}
