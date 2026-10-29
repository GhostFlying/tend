package protocol

import (
	"slices"
	"strings"
)

// CapabilityAcceptanceExecution negotiates optional acceptance model selection.
const CapabilityAcceptanceExecution = "acceptance-execution-v1"

// ExecutionProfile selects a model independently from another worker role.
type ExecutionProfile struct {
	Model  string `json:"model"`
	Effort string `json:"effort,omitempty"`
}

// Validate rejects a role override that the launcher would silently ignore.
func (p ExecutionProfile) Validate() error {
	if strings.TrimSpace(p.Model) == "" || p.Model != strings.TrimSpace(p.Model) || len(p.Model) > 200 || !validExecutionEffort(p.Effort) {
		return Errorf(CodeInvalidRequest, "execution selection requires a model and a supported reasoning effort")
	}
	return nil
}

func validExecutionEffort(effort string) bool {
	return slices.Contains([]string{"", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}, effort)
}
