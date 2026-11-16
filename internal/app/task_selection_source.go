package app

import (
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
)

// freezeTaskSelectionSource replaces caller-supplied attribution before system
// defaults/pins are applied. Owner authority delegated to an Agent is still an
// Agent-authored selection; quoting an owner message cannot change that fact.
func freezeTaskSelectionSource(x *Exec, spec *domain.Spec) {
	actor := x.Principal
	if x.Actor != nil {
		actor = *x.Actor
	}
	source := &protocol.TaskSelectionSource{Actor: actor, RequestID: x.Env.RequestID, HostID: spec.Requirements.HostID}
	if spec.AgentSelection != nil {
		selection := spec.AgentSelection.Clone()
		source.AgentSelection = &selection
	}
	if spec.AcceptanceExecution != nil {
		profile := *spec.AcceptanceExecution
		source.AcceptanceExecution = &profile
	}
	spec.SelectionSource = source
}
