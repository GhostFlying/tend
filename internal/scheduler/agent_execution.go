package scheduler

import (
	"cmp"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
)

type Executor struct {
	Pin        protocol.RunExecutionPin
	Categories []string
}

// SelectExecution checks both roles on one backend before a first allocation.
// Only the current run's choice is returned and subsequently frozen by app.
func SelectExecution(h Host, c Candidate) (*protocol.RunExecutionPin, string) {
	if c.Manager != nil {
		if c.ManagerPin != nil {
			if c.ManagerPin.Identity.ExecutionEnvironmentID != h.ID {
				return nil, "manager session belongs to another runner"
			}
			if c.Manager.ReceiptOnly {
				pin := *c.ManagerPin
				return &pin, ""
			}
			for _, e := range h.Executors {
				original, observed := *c.ManagerPin, e.Pin
				// An unrelated inventory edit changes its enclosing receipt,
				// not this definition/runtime revision. Continue with the
				// original retained snapshot; the runner resolves that exact
				// snapshot again before opening the native session.
				original.SnapshotID, observed.SnapshotID = "", ""
				original.InventoryRevision, observed.InventoryRevision = 0, 0
				original.PublicSHA256, observed.PublicSHA256 = "", ""
				if observed == original && slices.Contains(e.Categories, c.Manager.CategoryID) {
					pin := *c.ManagerPin
					return &pin, ""
				}
			}
			return nil, "original manager execution is unavailable"
		}
		var selected *protocol.RunExecutionPin
		for _, e := range h.Executors {
			if e.Pin.Purpose != "issue_manager" || !slices.Contains(e.Categories, c.Manager.CategoryID) {
				continue
			}
			if selected == nil || e.Pin.Identity.LocalID < selected.Identity.LocalID {
				p := e.Pin
				selected = &p
			}
		}
		if selected == nil {
			return nil, "no available issue-manager category candidate"
		}
		return selected, ""
	}
	if c.AgentSelection == nil {
		return nil, ""
	}
	role := c.AgentSelection.Implementation
	if c.Kind == protocol.ActionAccept {
		role = c.AgentSelection.Acceptance
	}
	matches := func(e Executor, r protocol.AgentRequirement, accept bool) bool {
		return e.Pin.Purpose != "issue_manager" && r.MatchesProfile(e.Pin.Profile) && e.Pin.Identity.ExecutionEnvironmentID == h.ID && slices.Contains(e.Categories, r.CategoryID) && (r.RuntimeID == "" || r.RuntimeID == e.Pin.Runtime.RuntimeID) && (c.WorkspaceBackendID == "" || c.WorkspaceBackendID == e.Pin.BackendID) && (c.Mounts <= 1 || e.Pin.BackendID == "git") && (!accept || c.AcceptanceProfile == nil || *c.AcceptanceProfile == e.Pin.Profile)
	}
	executors := slices.Clone(h.Executors)
	slices.SortFunc(executors, func(a, b Executor) int {
		if n := cmp.Compare(a.Pin.Identity.LocalID, b.Pin.Identity.LocalID); n != 0 {
			return n
		}
		if n := cmp.Compare(a.Pin.Runtime.RuntimeID, b.Pin.Runtime.RuntimeID); n != 0 {
			return n
		}
		return cmp.Compare(a.Pin.BackendID, b.Pin.BackendID)
	})
	for _, e := range executors {
		if !matches(e, role, c.Kind == protocol.ActionAccept) {
			continue
		}
		if c.Kind == protocol.ActionImplement && (c.WorkspaceHost == "" || !c.NamedExecutionAssigned) {
			found := false
			for _, review := range executors {
				if review.Pin.BackendID == e.Pin.BackendID && matches(review, c.AgentSelection.Acceptance, true) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		pin := e.Pin
		return &pin, ""
	}
	return nil, "no available category/runtime/model/effort candidates satisfy this host and workspace backend"
}
