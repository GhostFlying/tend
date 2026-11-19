//go:build linux

package updater

import (
	"context"
	"errors"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

type retirementAuthority struct {
	*fakeAuthority
	ready     map[string]bool
	loseAck   bool
	confirmed map[string]int
}

func (a *retirementAuthority) AuthorizeLaunch(ctx context.Context, _ Version, b Binding, _ RollingLaunch) (Grant, error) {
	return a.Authorize(ctx, b)
}
func (a *retirementAuthority) Retire(_ context.Context, _ string, in protocol.RunnerRetirement) (protocol.RunnerRetirementResult, error) {
	result := protocol.RunnerRetirementResult{RunnerRetirement: in}
	if !a.ready[in.Process.LaunchID] {
		return result, nil
	}
	result.Allowed = true
	result.AuthorizationID = "stop-" + in.Process.LaunchID
	if in.AuthorizationID != "" {
		if in.AuthorizationID != result.AuthorizationID {
			return result, ErrUnknown
		}
		a.confirmed[in.Process.LaunchID]++
		if a.loseAck {
			a.loseAck = false
			return result, errors.New("lost stop acknowledgement")
		}
		result.Confirmed = true
	}
	return result, nil
}

// Two older processes are observations. The call to Step, exact stop, durable
// stop record, supervisor reopen and acknowledgment all follow normal paths.
func TestRollingRetirementResumesLostAckWithoutStoppingAnotherProcess(t *testing.T) {
	h := newHarness(t)
	auth := &retirementAuthority{fakeAuthority: h.a, ready: map[string]bool{}, confirmed: map[string]int{}}
	h.opts.RollingAuthority = auth
	h.s.opts.RollingAuthority = auth
	for _, id := range []Identity{{PID: 11, StartTicks: 101, LaunchID: "first", SHA256: h.s.state.Process.SHA256}, {PID: 12, StartTicks: 102, LaunchID: "middle", SHA256: h.s.state.Process.SHA256}} {
		h.p.live[id.PID] = id
		if h.s.state.DrainingProcesses == nil {
			h.s.state.DrainingProcesses = map[string]DrainingProcess{}
		}
		h.s.state.DrainingProcesses[id.LaunchID] = DrainingProcess{Process: id, UpgradeID: "replace-" + id.LaunchID}
	}
	if err := h.s.criticalState(); err != nil {
		t.Fatal(err)
	}
	h.s.state.Format = 6
	h.s.state.CurrentJournal = "third"
	if err := h.s.save(); err != nil {
		t.Fatal(err)
	}
	h.reopen(t)
	h.step(t)
	if h.p.stops != 0 {
		t.Fatal("live work was stopped")
	}
	auth.ready["first"] = true
	auth.loseAck = true
	if err := h.s.Step(t.Context()); err == nil {
		t.Fatal("lost acknowledgement went unobserved")
	}
	if h.p.stops != 1 || !h.s.Status().DrainingProcesses["first"].Stopped || len(h.p.live) != 2 {
		t.Fatalf("incorrect retirement: %+v", h.s.Status())
	}
	h.reopen(t)
	h.step(t)
	if h.p.stops != 1 || len(h.s.Status().DrainingProcesses) != 1 || auth.confirmed["first"] != 2 {
		t.Fatal("stopped predecessor was not reconciled exactly once")
	}
	// Its PID was subsequently reused. No signal may reach the newer process.
	replaced := h.p.live[12]
	replaced.StartTicks++
	h.p.live[12] = replaced
	auth.ready["middle"] = true
	h.step(t)
	if h.p.live[12] != replaced || h.p.stops != 1 || len(h.s.Status().DrainingProcesses) != 0 {
		t.Fatal("PID reuse affected another process")
	}
}
