package scheduler

import (
	"cmp"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
)

type Executor struct {
	Continuation bool
	Pin          protocol.RunExecutionPin
	Categories   []string
}

// SelectExecution checks both roles on one backend before a first allocation.
// Only the current run's choice is returned and subsequently frozen by app.
func SelectExecution(h Host, c Candidate) (*protocol.RunExecutionPin, string) {
	if c.HandoffPin != nil {
		if h.ConcurrencyBlocked[c.HandoffPin.Runtime.RuntimeID] {
			return nil, "runtime concurrency capacity unavailable"
		}
		if c.HandoffPin.Identity.ExecutionEnvironmentID != h.ID {
			return nil, "handoff belongs to another runner"
		}
		for _, e := range append(slices.Clone(h.Executors), h.Continuations...) {
			original, observed := *c.HandoffPin, e.Pin
			original.SnapshotID, observed.SnapshotID = "", ""
			original.InventoryRevision, observed.InventoryRevision = 0, 0
			original.PublicSHA256, observed.PublicSHA256 = "", ""
			if observed == original {
				pin := *c.HandoffPin
				return &pin, ""
			}
		}
		return nil, "original handoff execution is unavailable"
	}
	if c.Manager != nil {
		if c.Manager.OwnershipPolicy == protocol.IssueOwnerPolicy && !h.IssueOwners && !c.Manager.ReceiptOnly {
			return nil, "runner does not support Issue owner responsibility"
		}
		if c.ManagerPin != nil {
			if !c.Manager.ReceiptOnly && h.ConcurrencyBlocked[c.ManagerPin.Runtime.RuntimeID] {
				return nil, "runtime concurrency capacity unavailable"
			}
			if c.ManagerPin.Identity.ExecutionEnvironmentID != h.ID {
				return nil, "manager session belongs to another runner"
			}
			if c.Manager.ReceiptOnly {
				pin := *c.ManagerPin
				return &pin, ""
			}
			for _, e := range h.Continuations {
				if e.Pin == *c.ManagerPin {
					pin := e.Pin
					return &pin, ""
				}
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
				if observed == original {
					pin := *c.ManagerPin
					return &pin, ""
				}
			}
			return nil, "original manager execution is unavailable"
		}
		var selected *protocol.RunExecutionPin
		for _, e := range h.Executors {
			if h.ConcurrencyBlocked[e.Pin.Runtime.RuntimeID] || e.Pin.Purpose != "issue_manager" || c.Manager.AgentID == "" && !slices.Contains(e.Categories, c.Manager.CategoryID) || c.Manager.AgentID != "" && c.Manager.AgentID != e.Pin.Identity.ExecutionEnvironmentID+"/"+e.Pin.Identity.LocalID {
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
	if c.OwnerAgentRevision != 0 && !h.IssueOwners {
		return nil, "runner does not support Issue owner execution contracts"
	}
	role := c.AgentSelection.Implementation
	if c.Kind == protocol.ActionAccept {
		role = c.AgentSelection.Acceptance
	}
	matches := func(e Executor, r protocol.AgentRequirement, accept bool) bool {
		retained := c.OwnerPin
		if accept {
			retained = c.ReviewOwnerPin
		}
		if e.Continuation && (retained == nil || *retained != e.Pin) {
			return false
		}
		if h.ContinuationsSupported && retained != nil && e.Pin != *retained {
			return false
		}
		if h.ContinuationsSupported && !accept && c.OwnerAgentRevision != 0 && c.OwnerPin == nil {
			return false
		}
		if !accept && c.OwnerAgentRevision != 0 && (e.Pin.AgentRevision != c.OwnerAgentRevision || e.Pin.Runtime.Revision != c.OwnerRuntimeRevision) {
			return false
		}
		if accept && c.ReviewOwnerPin != nil && (e.Pin.Identity != c.ReviewOwnerPin.Identity || e.Pin.AgentRevision != c.ReviewOwnerPin.AgentRevision || e.Pin.Runtime != c.ReviewOwnerPin.Runtime || e.Pin.Profile != c.ReviewOwnerPin.Profile) {
			return false
		}
		concrete := r.AgentID != "" || retained != nil && retained.Identity == e.Pin.Identity
		return e.Pin.Purpose != "issue_manager" && (r.AgentID == "" || r.AgentID == e.Pin.Identity.ExecutionEnvironmentID+"/"+e.Pin.Identity.LocalID) && r.MatchesProfile(e.Pin.Profile) && e.Pin.Identity.ExecutionEnvironmentID == h.ID && (concrete || slices.Contains(e.Categories, r.CategoryID)) && (r.RuntimeID == "" || r.RuntimeID == e.Pin.Runtime.RuntimeID) && (c.WorkspaceBackendID == "" || c.WorkspaceBackendID == e.Pin.BackendID) && (c.Mounts <= 1 || e.Pin.BackendID == "git") && (!accept || c.AcceptanceProfile == nil || *c.AcceptanceProfile == e.Pin.Profile)
	}
	executors := append(slices.Clone(h.Executors), h.Continuations...)
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
		if h.ConcurrencyBlocked[e.Pin.Runtime.RuntimeID] || !matches(e, role, c.Kind == protocol.ActionAccept) {
			continue
		}
		if c.Kind == protocol.ActionImplement && !c.AcceptanceWaived && (c.WorkspaceHost == "" || !c.NamedExecutionAssigned) {
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
