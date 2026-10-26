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
		{"required other host", nil, func(c *Candidate) { c.Requirements.HostID = "other" }, implement, "task requires another host"},
		{"required own host", nil, func(c *Candidate) { c.Requirements.HostID = "linux-1" }, implement, ""},
		{"eligible implement", nil, nil, implement, ""},
		{"eligible accept", nil, nil, accept, ""},
		{"observation implement unsupported", nil, func(c *Candidate) { c.RequiresObservation = true }, implement, "runner does not support scoped worker observation"},
		{"observation acceptance unsupported", nil, func(c *Candidate) { c.RequiresObservation = true }, accept, "runner does not support scoped worker observation"},
		{"observation capable", func(h *Host) { h.WorkerObservation = true }, func(c *Candidate) { c.RequiresObservation = true }, accept, ""},
		{"input implement unsupported", nil, func(c *Candidate) { c.RequiresTaskInputs = true }, implement, "runner does not support task input attachments"},
		{"input acceptance unsupported", nil, func(c *Candidate) { c.RequiresTaskInputs = true }, accept, "runner does not support task input attachments"},
		{"input capable", func(h *Host) { h.TaskInputs = true }, func(c *Candidate) { c.RequiresTaskInputs = true }, accept, ""},
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
		{"abandoned host", nil, func(c *Candidate) { c.AbandonedHosts = []string{"mac-1", "linux-1"} }, implement, "task gave up its workspace on this host"},
		{"other host abandoned", nil, func(c *Candidate) { c.AbandonedHosts = []string{"mac-1"} }, implement, ""},
		{"several mounts, single-mount worker", nil, func(c *Candidate) { c.Mounts = 2 }, implement, MultiMountUnsupported},
		{"several mounts, multi-mount worker", func(h *Host) { h.Worker.MultiMount = true }, func(c *Candidate) { c.Mounts = 2 }, accept, ""},
		{"one mount, single-mount worker", nil, func(c *Candidate) { c.Mounts = 1 }, implement, ""},
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
	o := Order{Now: 1000, WaitPerHour: 0.1}
	low, high, older, sameOlderID := implement(), implement(), implement(), implement()
	low, unrated := implement(), implement()
	low.ActionID, low.Rating, low.CreatedAt = "act_low", protocol.RatingP3, 1
	unrated.ActionID, unrated.CreatedAt = "act_unrated", 2
	high.ActionID, high.Rating, high.CreatedAt = "act_high_new", protocol.RatingP1, 300
	older.ActionID, older.Rating, older.CreatedAt = "act_high_old", protocol.RatingP1, 200
	sameOlderID.ActionID, sameOlderID.Rating, sameOlderID.CreatedAt = "act_high_aaa", protocol.RatingP1, 200
	blocked := implement()
	blocked.ActionID, blocked.Rating, blocked.Block = "act_blocked", protocol.RatingP0, protocol.BlockWaitingDecision

	occupied := implement()
	occupied.ActionID, occupied.Rating, occupied.WorkspaceOccupied = "act_occupied", protocol.RatingP0, true
	if Held(occupied) == "" {
		t.Error("a busy workspace does not hold its task on every host")
	}

	got, ok := Pick(linux, []Candidate{low, high, older, blocked, sameOlderID, unrated, occupied}, o)
	if !ok || got.ActionID != "act_high_aaa" {
		t.Errorf("Pick = %s (ok=%v), want act_high_aaa", got.ActionID, ok)
	}
	if _, ok := Pick(linux, []Candidate{blocked}, o); ok {
		t.Error("Pick returned an ineligible candidate")
	}
	if _, ok := Pick(linux, nil, o); ok {
		t.Error("Pick returned a candidate from nothing")
	}
}

// TestScoreOrder walks through the examples of the phase D plan (§6).
func TestScoreOrder(t *testing.T) {
	const hour = int64(3_600_000)
	o := Order{Now: 100 * hour, WaitPerHour: 0.1}
	queued := func(id string, weight int64, r protocol.Rating, waited int64) Candidate {
		c := implement()
		c.ActionID, c.Weight, c.Rating, c.CreatedAt = id, weight, r, o.Now-waited
		return c
	}
	cases := []struct {
		name  string
		order Order
		a, b  Candidate
		want  string
	}{
		{"weight multiplies the rating", o, queued("a", 3, protocol.RatingP3, 0), queued("b", 1, protocol.RatingP2, 0), "a"},
		{"waiting raises a score", o, queued("a", 3, protocol.RatingP3, 0), queued("b", 1, protocol.RatingP2, 11*hour), "b"},
		{"unrated counts as p2", o, queued("a", 1, protocol.RatingP2, 0), queued("b", 1, protocol.RatingNone, hour), "b"},
		{"p0 comes first", o, queued("a", 9, protocol.RatingP1, 50*hour), queued("b", 1, protocol.RatingP0, 0), "b"},
		{"older p0 first", o, queued("a", 1, protocol.RatingP0, 0), queued("b", 9, protocol.RatingP0, hour), "b"},
		// Without aging, equal scores go by waiting time.
		{"equal scores, older first", Order{Now: o.Now}, queued("a", 3, protocol.RatingP3, 0), queued("b", 1, protocol.RatingP1, hour), "b"},
		{"same score and age", o, queued("b", 3, protocol.RatingP3, 0), queued("a", 1, protocol.RatingP1, 0), "a"},
	}
	for _, tc := range cases {
		got := tc.b.ActionID
		if Compare(tc.a, tc.b, tc.order) < 0 {
			got = tc.a.ActionID
		}
		if got != tc.want {
			t.Errorf("%s: %s first (scores %.2f, %.2f), want %s", tc.name, got, Score(tc.a, tc.order), Score(tc.b, tc.order), tc.want)
		}
		if Compare(tc.a, tc.b, tc.order) != -Compare(tc.b, tc.a, tc.order) {
			t.Errorf("%s: Compare is not antisymmetric", tc.name)
		}
	}
	// Among equal scores, acceptance moves a task along first.
	impl, acc := queued("act_0", 1, protocol.RatingP2, hour), accept()
	acc.Weight, acc.CreatedAt = 1, impl.CreatedAt+1
	if Compare(acc, impl, Order{Now: o.Now}) >= 0 {
		t.Error("equal scores: implementation before acceptance")
	}
}
