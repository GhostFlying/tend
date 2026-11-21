//go:build linux

package updater

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

type retirementAuthority struct {
	*fakeAuthority
	sequence  int64
	ready     map[string]bool
	loseAck   bool
	confirmed map[string]int
}

func (a *retirementAuthority) AuthorizeLaunch(ctx context.Context, _ Version, b Binding, _ RollingLaunch, _ string) (Grant, error) {
	return a.Authorize(ctx, b)
}
func (a *retirementAuthority) Retire(_ context.Context, _ string, in protocol.RunnerRetirement) (protocol.RunnerRetirementResult, error) {
	result := protocol.RunnerRetirementResult{RunnerRetirement: in}
	if !a.ready[in.Process.LaunchID] {
		return result, nil
	}
	result.Allowed = true
	result.ReadySequence = a.sequence
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
	if h.p.stops != 1 || len(h.s.Status().DrainingProcesses) != 1 || auth.confirmed["first"] != 2 || len(h.s.Status().RetiredJournals) != 1 || h.s.Status().RetiredJournals[0] != "" {
		t.Fatal("stopped predecessor was not reconciled exactly once")
	}
	// A durable intent can precede signal dispatch. Reopening retains that
	// uncertainty until a newer ready heartbeat from the same live runner.
	third := Identity{PID: 13, StartTicks: 103, LaunchID: "interrupted", SHA256: h.s.state.Process.SHA256}
	h.p.live[third.PID] = third
	auth.ready[third.LaunchID] = true
	h.s.state.DrainingProcesses[third.LaunchID] = DrainingProcess{Process: third, UpgradeID: "replace-interrupted", AuthorizationID: "stop-interrupted", StopRequested: true, StopSequence: 4}
	if err := h.s.save(); err != nil {
		t.Fatal(err)
	}
	h.reopen(t)
	auth.sequence = 4
	if err := h.s.Step(t.Context()); !errors.Is(err, ErrUnknown) || h.p.stops != 1 {
		t.Fatal("ambiguous signal repeated", err)
	}
	auth.sequence = 5
	h.step(t)
	if h.p.stops != 2 || h.p.live[13].PID != 0 || auth.confirmed["interrupted"] != 1 {
		t.Fatal("verified live predecessor failed to retire")
	}
	// Its PID was subsequently reused. No signal may reach the newer process.
	replaced := h.p.live[12]
	replaced.StartTicks++
	h.p.live[12] = replaced
	auth.ready["middle"] = true
	h.step(t)
	if h.p.live[12] != replaced || h.p.stops != 2 || len(h.s.Status().DrainingProcesses) != 0 {
		t.Fatal("PID reuse affected another process")
	}
}

// The fake signal immediately follows its final grant check.
func (p *fakeProcess) StopIf(ctx context.Context, id Identity, timeout time.Duration, check func() error) error {
	if err := check(); err != nil {
		return err
	}
	return p.Stop(ctx, id, timeout)
}

type slowPinnedProcess struct {
	*fakeProcess
	pin func()
}

func (p slowPinnedProcess) StopIf(ctx context.Context, id Identity, timeout time.Duration, check func() error) error {
	p.pin()
	return p.fakeProcess.StopIf(ctx, id, timeout, check)
}

type windowStopAuthority struct {
	*retirementAuthority
	afterGrant func()
	declines   int
}

func (a *windowStopAuthority) Retire(ctx context.Context, journal string, in protocol.RunnerRetirement) (protocol.RunnerRetirementResult, error) {
	if in.WindowID == "" {
		result := protocol.RunnerRetirementResult{RunnerRetirement: in}
		result.WindowID = "window"
		return result, nil
	}
	if in.Unused {
		a.declines++
		result := protocol.RunnerRetirementResult{RunnerRetirement: in}
		if a.declines == 1 {
			return result, errors.New("lost unused receipt")
		}
		return result, nil
	}
	result, err := a.retirementAuthority.Retire(ctx, journal, in)
	if in.AuthorizationID == "" {
		result.RemainingMillis = 500
		if a.afterGrant != nil {
			a.afterGrant()
		}
	}
	return result, err
}

func TestRollingWindowGrantUsesOriginalDispatchCutoff(t *testing.T) {
	for _, stage := range []string{"response_after_deadline", "pin_after_deadline", "dispatch_before_deadline"} {
		t.Run(stage, func(t *testing.T) {
			h := newHarness(t)
			id := Identity{PID: 11, StartTicks: 101, LaunchID: "first", SHA256: h.s.state.Process.SHA256}
			auth := &windowStopAuthority{retirementAuthority: &retirementAuthority{fakeAuthority: h.a, ready: map[string]bool{id.LaunchID: true}, confirmed: map[string]int{}}}
			if stage == "pin_after_deadline" {
				h.opts.Process = slowPinnedProcess{h.p, func() { h.boot += 501 }}
				h.s.opts.Process = h.opts.Process
			}
			if stage == "response_after_deadline" {
				auth.afterGrant = func() { h.boot += 501 }
			}
			h.opts.RollingAuthority = auth
			h.s.opts.RollingAuthority = auth
			h.s.state.Format = 6
			h.s.state.CurrentJournal = "current"
			h.s.state.DrainingProcesses = map[string]DrainingProcess{id.LaunchID: {Process: id, UpgradeID: "handoff"}}
			h.p.live[id.PID] = id
			if err := h.s.criticalState(); err != nil {
				t.Fatal(err)
			}
			if err := h.s.save(); err != nil {
				t.Fatal(err)
			}
			if stage == "dispatch_before_deadline" {
				h.step(t)
				if h.p.stops != 1 || len(h.s.Status().DrainingProcesses) != 0 || auth.confirmed[id.LaunchID] != 1 {
					t.Fatal(h.s.Status())
				}
				return
			}
			if err := h.s.Step(t.Context()); err == nil {
				t.Fatal("missing unused receipt")
			}
			h.reopen(t)
			h.step(t)
			record := h.s.Status().DrainingProcesses[id.LaunchID]
			if h.p.stops != 0 || auth.declines != 2 || record.Process != id || record.StopRequested || record.AuthorizationID != "" {
				t.Fatal("late grant stopped original process", record)
			}
		})
	}
}

type primaryWindowAuthority struct{ *windowStopAuthority }

func (a primaryWindowAuthority) Target(context.Context, Identity) (string, error) {
	return "window", nil
}
func (a primaryWindowAuthority) Retire(ctx context.Context, in protocol.RunnerRetirement) (protocol.RunnerRetirementResult, error) {
	return a.windowStopAuthority.Retire(ctx, "", in)
}

func TestEmergencyPrimaryReopensWithoutRespawningAfterLostExitReceipt(t *testing.T) {
	h := newHarness(t)
	original := h.s.Status().Process
	a := &retirementAuthority{fakeAuthority: h.a, sequence: 3, ready: map[string]bool{original.LaunchID: true}, loseAck: true, confirmed: map[string]int{}}
	h.opts.WindowAuthority = primaryWindowAuthority{&windowStopAuthority{retirementAuthority: a}}
	h.reopen(t)
	if err := h.s.discoverEmergency(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Step(t.Context()); err == nil {
		t.Fatal("expected interrupted receipt")
	}
	if s := h.s.Status().EmergencyPrimary; s == nil || !s.Stopped || s.Confirmed || s.Process != original {
		t.Fatal(s)
	}
	h.reopen(t)
	h.step(t)
	h.reopen(t)
	h.step(t)
	if len(h.p.live) != 0 || h.p.stops != 1 || !h.s.Status().EmergencyPrimary.Confirmed || a.confirmed[original.LaunchID] != 2 {
		t.Fatal("lost original physical exit", h.s.Status())
	}
}

func TestEmergencyDrainerUsesIndependentHostConnection(t *testing.T) {
	h := newHarness(t)
	current := h.s.Status().Process
	old := Identity{PID: 11, StartTicks: 101, LaunchID: "old", SHA256: current.SHA256}
	h.p.live[old.PID] = old
	auth := &windowStopAuthority{retirementAuthority: &retirementAuthority{fakeAuthority: h.a, ready: map[string]bool{old.LaunchID: true}, confirmed: map[string]int{}}}
	h.s.opts.WindowAuthority = primaryWindowAuthority{auth}
	// The accepting runner has no rolling bridge. Its independent supervisor
	// can nevertheless retire the incompatible predecessor.
	h.s.state.DrainingProcesses = map[string]DrainingProcess{old.LaunchID: {Process: old, UpgradeID: "handoff"}}
	if err := h.s.save(); err != nil {
		t.Fatal(err)
	}
	h.step(t)
	if h.p.stops != 1 || h.p.live[current.PID] != current || len(h.s.Status().DrainingProcesses) != 0 || auth.confirmed[old.LaunchID] != 1 {
		t.Fatal(h.s.Status())
	}
}

type crashWindowAuthority struct {
	primaryWindowAuthority
	target string
	err    error
}

func (a *crashWindowAuthority) Target(context.Context, Identity) (string, error) {
	return a.target, a.err
}

func TestEmergencyPrimaryExitBeforeDiscovery(t *testing.T) {
	for _, point := range []string{"before_monitor", "before_launch", "unavailable", "ordinary"} {
		t.Run(point, func(t *testing.T) {
			h := newHarness(t)
			old := h.s.Status().Process
			a := &retirementAuthority{fakeAuthority: h.a, sequence: 10, ready: map[string]bool{old.LaunchID: true}, confirmed: map[string]int{}}
			control := &crashWindowAuthority{primaryWindowAuthority: primaryWindowAuthority{&windowStopAuthority{retirementAuthority: a}}}
			h.s.opts.WindowAuthority = control
			delete(h.p.live, old.PID)
			if point == "ordinary" {
				h.step(t)
				h.step(t)
				if h.s.Status().Process == old || h.s.Status().Operation.Phase != "committed" {
					t.Fatal(h.s.Status())
				}
				return
			}
			if point == "before_launch" {
				h.step(t)
				if h.s.Status().Operation.Phase != "backed_up" {
					t.Fatal(h.s.Status())
				}
			}
			if point == "unavailable" {
				control.err = errors.New("test channel offline")
				if err := h.s.Step(t.Context()); err == nil || h.s.Status().Operation != nil {
					t.Fatal("ambiguous crash restart", err)
				}
				control.err = nil
			}
			control.target = "window"
			h.step(t)
			state := h.s.Status()
			if state.EmergencyPrimary == nil || !state.EmergencyPrimary.Confirmed || state.Process != old || len(h.p.live) > 0 || h.p.stops != 0 || !done(state.Operation) {
				t.Fatal("dead primary was restarted or signalled", state)
			}
		})
	}
}
