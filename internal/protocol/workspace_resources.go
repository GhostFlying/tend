package protocol

const (
	OpWorkspaceRelease     = "runner.workspaces_release"
	OpWorkspaceCleanup     = "runner.workspaces_cleanup"
	OpWorkspaceCleanupDone = "runner.workspaces_cleanup_done"
)

// WorkspaceRelease confirms that a task's local processes have stopped.
type WorkspaceRelease struct {
	WorkspaceID string `json:"workspace_id"`
	TaskID      string `json:"task_id"`
}

// WorkspaceReleaseResult remains false while the task is not terminal.
type WorkspaceReleaseResult struct {
	Released bool `json:"released"`
}

// WorkspaceCleanupRequest reserves eligible resources for removal.
type WorkspaceCleanupRequest struct {
	Limit int `json:"limit,omitempty"`
}

// WorkspaceCleanup is an immutable, host-bound removal authorization.
type WorkspaceCleanup struct {
	CleanupID          string `json:"cleanup_id"`
	WorkspaceID        string `json:"workspace_id"`
	Kind               string `json:"kind"`
	Name               string `json:"name"`
	Path               string `json:"path"`
	ProviderID         string `json:"provider_id,omitempty"`
	ProviderInstanceID string `json:"provider_instance_id,omitempty"`
}

type WorkspaceCleanupResult struct {
	Workspaces []WorkspaceCleanup `json:"workspaces"`
}

type WorkspaceCleanupDone struct {
	CleanupID   string `json:"cleanup_id"`
	WorkspaceID string `json:"workspace_id"`
}
