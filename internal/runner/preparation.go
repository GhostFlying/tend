package runner

import "github.com/GhostFlying/tend/internal/protocol"

// PreparationError proves that workspace preparation stopped before launching
// this run's worker. Only the workspace adapter constructs this known failure.
type PreparationError struct {
	Failure protocol.RunPreparationFailure
}

func (e *PreparationError) Error() string {
	return "inherited changes require workspace.include_existing_changes before a commit task"
}
