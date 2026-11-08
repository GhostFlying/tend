package protocol

// LocalCodexEnvironmentRequest never crosses the server/runner wire.
type LocalCodexEnvironmentRequest struct {
	After            string `json:"after,omitempty"`
	Limit            int    `json:"limit,omitempty"`
	Root             string `json:"root"`
	Action           string `json:"action"`
	ID               string `json:"id,omitempty"`
	RequestID        string `json:"request_id,omitempty"`
	ExpectedRevision uint64 `json:"expected_control_revision"`
	Data             string `json:"data,omitempty"`
	ItemID           string `json:"item_id,omitempty"`
	Source           string `json:"source,omitempty"`
	Generation       string `json:"generation,omitempty"`
	DryRun           bool   `json:"dry_run,omitempty"`
}

func CodexEnvironmentActions() []string {
	return []string{"list", "show", "inventory", "doctor", "create", "update", "delete", "plan", "sync", "rollback", "reconcile", "skill add", "skill update", "skill remove", "plugin add", "plugin update", "plugin remove", "watch enable", "watch disable", "watch status", "watch run"}
}
