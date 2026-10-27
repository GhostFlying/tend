package traex

import "github.com/GhostFlying/tend/internal/harness"

func (t *thread) setPermissionDefaults(res ThreadStartResponse) {
	policy := harness.PermissionSettings{Sandbox: res.Sandbox.Type, ApprovalPolicy: res.ApprovalPolicy, ApprovalsReviewer: res.ApprovalsReviewer}
	switch policy.Sandbox {
	case "workspaceWrite":
		policy.Sandbox = "workspace-write"
	case "readOnly":
		policy.Sandbox = "read-only"
	case "dangerFullAccess":
		policy.Sandbox = "danger-full-access"
	}
	// Native versions may return the legacy spelling for auto_review.
	if policy.ApprovalsReviewer == "guardian_subagent" {
		policy.ApprovalsReviewer = "auto_review"
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.permissions = policy
}

// PermissionDefaults implements harness.PermissionedThread. Values are from
// the native response and are not filled from the requested ThreadConfig.
func (t *thread) PermissionDefaults() harness.PermissionSettings {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.permissions
}
