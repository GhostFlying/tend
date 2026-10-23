package protocol

// OpWorkspaceReconcile lists stopped ledger runs whose local agents still
// require runner confirmation before the resource can be reused.
const OpWorkspaceReconcile = "runner.workspaces_reconcile"

// WorkspaceReconcile identifies every actual run the runner must reconcile.
// Missing local records never constitute proof that the agents stopped.
type WorkspaceReconcile struct {
	WorkspaceID string   `json:"workspace_id"`
	TaskID      string   `json:"task_id"`
	RunIDs      []string `json:"run_ids"`
	// TaskTerminal distinguishes ended owners from detached resources of tasks
	// that may still run. It does not prove the resource's native process stopped.
	TaskTerminal bool `json:"task_terminal,omitempty"`
	// FinishedRunIDs identifies successful final reports, which prove the
	// legacy app-server worker returned from its closed per-run session.
	FinishedRunIDs []string `json:"finished_run_ids,omitempty"`
}

// WorkspaceReconcileResult contains host-bound occupied resources.
type WorkspaceReconcileResult struct {
	Workspaces []WorkspaceReconcile `json:"workspaces"`
}
