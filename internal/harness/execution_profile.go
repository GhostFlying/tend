package harness

import "github.com/GhostFlying/tend/internal/protocol"

// WorkerProfile resolves a whole role-specific selection; an omitted effort
// leaves that model's native default rather than inheriting another model's.
func WorkerProfile(spec WorkerSpec, model, effort string, acceptance *protocol.ExecutionProfile) protocol.ExecutionProfile {
	profile := protocol.ExecutionProfile{Model: model, Effort: effort}
	if spec.Role == protocol.RoleAccept {
		if acceptance != nil {
			profile = *acceptance
		}
		if spec.Execution != nil {
			profile = *spec.Execution
		}
	}
	return profile
}
