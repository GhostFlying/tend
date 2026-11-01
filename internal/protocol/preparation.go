package protocol

const (
	PreparationPhaseWorkspace   = "workspace_prepare"
	PreparationInheritedChanges = "inherited_changes_not_included"
	PreparationIncludeField     = "workspace.include_existing_changes"
	MaxPreparationChanges       = 64
)

// RunPreparationFailure is bounded evidence from a failed preparation, never
// permission to absorb changes or retry an unknown native operation.
type RunPreparationFailure struct {
	Phase         string              `json:"phase"`
	Code          string              `json:"code"`
	WorkerStarted bool                `json:"worker_started"`
	WorkspaceID   string              `json:"workspace_id"`
	SourceTaskID  string              `json:"source_task_id"`
	RequiredField string              `json:"required_field"`
	Changes       []PreparationChange `json:"changes"`
	ChangeCount   int                 `json:"change_count"`
	Truncated     bool                `json:"truncated"`
}

// PreparationChange contains repository-relative status, without file contents,
// native paths, credentials or an inference about who authored the change.
type PreparationChange struct {
	Mount  string `json:"mount"`
	Path   string `json:"path"`
	Status string `json:"status"`
	HEAD   string `json:"head"`
}
