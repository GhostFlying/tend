package api

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
)

// toolAnswer is a tool's answer as the model reads it.
type toolAnswer struct {
	OK      bool            `json:"ok"`
	Result  json.RawMessage `json:"result"`
	Error   *protocol.Error `json:"error"`
	Current json.RawMessage `json:"current"`
}

func createArgs(title, workspace string) map[string]any {
	return map[string]any{"title": title, "workspace_name": workspace, "reason": "the owner asked for it", "spec": domain.Spec{
		Goal: title, Acceptance: []string{"works"}, Delivery: domain.DeliveryCommit, Workspace: domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}},
	}}
}

// created returns the task ID a successful tasks_create made.
func created(t *testing.T, a toolAnswer) string {
	t.Helper()
	var res struct {
		Outcome struct {
			TaskID string `json:"task_id"`
		} `json:"outcome"`
	}
	if !a.OK || json.Unmarshal(a.Result, &res) != nil || res.Outcome.TaskID == "" {
		t.Fatalf("tasks_create = %+v", a)
	}
	return res.Outcome.TaskID
}
