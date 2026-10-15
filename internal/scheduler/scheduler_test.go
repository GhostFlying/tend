package scheduler

import (
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
)

var linux = Host{ID: "linux-1", OS: "linux", Active: true, Labels: []string{"gpu"}, Repos: []string{"tend", "douyin"}, Slots: 2}

func implement() Candidate {
	return Candidate{
		ActionID: "act_1", TaskID: "task_1", Kind: protocol.ActionImplement, Status: protocol.ActionPending,
		Generation: 1, TargetSpecVersion: 1, CreatedAt: 100,
		Lifecycle: protocol.TaskQueued, Control: protocol.ControlManaged, Block: protocol.BlockNone,
		SpecVersion: 1, ImplementGeneration: 1, DependenciesDone: true, Repos: []string{"tend"},
	}
}

func accept() Candidate {
	c := implement()
	c.ActionID, c.Kind, c.TargetArtifactID = "act_2", protocol.ActionAccept, "art_1"
	c.Lifecycle, c.CurrentArtifactID, c.WorkspaceHost = protocol.TaskAwaitingAcceptance, "art_1", "linux-1"
	return c
}

func TestSkipRules(t *testing.T) {
	cases := []struct {
		name   string
		host   func(*Host)
		cand   func(*Candidate)
		base   func() Candidate
		reason string
	}{
		{"eligible implement", nil, nil, implement, ""},
		{"eligible accept", nil, nil, accept, ""},
		{"retired host", func(h *Host) { h.Active = false }, nil, implement, "host is retired"},
		{"no slot", func(h *Host) { h.Busy = 2 }, nil, implement, "host has no free slot"},
		{"dispatched", nil, func(c *Candidate) { c.Status = protocol.ActionDispatched }, implement, "action is not pending"},
		{"deliver", nil, func(c *Candidate) { c.Kind = protocol.ActionDeliver }, implement, "action is not runnable"},
		{"old spec", nil, func(c *Candidate) { c.SpecVersion = 2 }, implement, "action targets an older spec version"},
		{"cancelled", nil, func(c *Candidate) { c.Lifecycle = protocol.TaskCancelled }, implement, "task is not runnable in its lifecycle"},
		{"draft", nil, func(c *Candidate) { c.Lifecycle = protocol.TaskDraft }, implement, "task is not runnable in its lifecycle"},
		{"paused", nil, func(c *Candidate) { c.Control = protocol.ControlPaused }, implement, "task is not managed"},
		{"human control", nil, func(c *Candidate) { c.Control = protocol.ControlHumanControl }, implement, "task is not managed"},
		{"blocked", nil, func(c *Candidate) { c.Block = protocol.BlockWaitingDecision }, implement, "task is blocked"},
		{"dependencies", nil, func(c *Candidate) { c.DependenciesDone = false }, implement, "dependencies are not completed"},
		{"old generation", nil, func(c *Candidate) { c.ImplementGeneration = 2 }, implement, "implement action is not the current generation"},
		{"accept of old artifact", nil, func(c *Candidate) { c.CurrentArtifactID = "art_2" }, accept, "acceptance targets an artifact that is no longer under acceptance"},
		{"accept after changes", nil, func(c *Candidate) { c.Lifecycle = protocol.TaskChangesRequired }, accept, "acceptance targets an artifact that is no longer under acceptance"},
		{"os", nil, func(c *Candidate) { c.Requirements = domain.Requirements{OS: "windows"} }, implement, "host OS does not match"},
		{"label", nil, func(c *Candidate) { c.Requirements = domain.Requirements{Labels: []string{"gpu", "ios"}} }, implement, "host lacks a required label"},
		{"repo", nil, func(c *Candidate) { c.Repos = []string{"tend", "template-assembler"} }, implement, "host does not serve every repository"},
		{"implement on other host", nil, func(c *Candidate) { c.WorkspaceHost = "mac-1" }, implement, "task workspace is on another host"},
		{"accept on other host", nil, func(c *Candidate) { c.WorkspaceHost = "mac-1" }, accept, "task workspace is on another host"},
		{"workspace occupied", nil, func(c *Candidate) { c.WorkspaceOccupied = true }, implement, "a run may still be using the workspace"},
		{"accept in occupied workspace", nil, func(c *Candidate) { c.WorkspaceOccupied = true }, accept, "a run may still be using the workspace"},
		{"implement on own host", nil, func(c *Candidate) { c.WorkspaceHost = "linux-1" }, implement, ""},
	}
	for _, tc := range cases {
		h, c := linux, tc.base()
		if tc.host != nil {
			tc.host(&h)
		}
		if tc.cand != nil {
			tc.cand(&c)
		}
		if got := Skip(h, c); got != tc.reason {
			t.Errorf("%s: Skip = %q, want %q", tc.name, got, tc.reason)
		}
	}
}

func TestPickOrder(t *testing.T) {
	low, high, older, sameOlderID := implement(), implement(), implement(), implement()
	low.ActionID, low.Priority, low.CreatedAt = "act_low", 0, 1
	high.ActionID, high.Priority, high.CreatedAt = "act_high_new", 5, 300
	older.ActionID, older.Priority, older.CreatedAt = "act_high_old", 5, 200
	sameOlderID.ActionID, sameOlderID.Priority, sameOlderID.CreatedAt = "act_high_aaa", 5, 200
	blocked := implement()
	blocked.ActionID, blocked.Priority, blocked.Block = "act_blocked", 9, protocol.BlockWaitingDecision

	got, ok := Pick(linux, []Candidate{low, high, older, blocked, sameOlderID})
	if !ok || got.ActionID != "act_high_aaa" {
		t.Errorf("Pick = %s (ok=%v), want act_high_aaa", got.ActionID, ok)
	}
	if _, ok := Pick(linux, []Candidate{blocked}); ok {
		t.Error("Pick returned an ineligible candidate")
	}
	if _, ok := Pick(linux, nil); ok {
		t.Error("Pick returned a candidate from nothing")
	}
}
