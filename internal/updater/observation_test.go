//go:build linux

package updater

import (
	"context"
	"errors"
	"testing"
)

type observingProcess struct {
	*fakeProcess
	checks int
	stamp  string
	err    error
}

func (p *observingProcess) Inspect(ctx context.Context, pid int) (Identity, error) {
	p.checks++
	return p.fakeProcess.Inspect(ctx, pid)
}
func (p *observingProcess) Observe(ctx context.Context, pid int) (ProcessObservation, error) {
	if p.err != nil {
		return ProcessObservation{}, p.err
	}
	id, err := p.fakeProcess.Inspect(ctx, pid)
	id.SHA256 = ""
	return ProcessObservation{Identity: id, Executable: p.stamp}, err
}
func TestStableSupervisionHashesOnceAndRevalidatesChanges(t *testing.T) {
	id := Identity{PID: 12, StartTicks: 34, LaunchID: "launch", SHA256: "sha"}
	p := &observingProcess{fakeProcess: &fakeProcess{live: map[int]Identity{12: id}}, stamp: "original"}
	s := &Supervisor{opts: Options{Process: p}}
	for range 60 {
		if got, err := s.observeCurrent(t.Context(), id); err != nil || got != id {
			t.Fatal(got, err)
		}
	}
	if p.checks != 1 {
		t.Fatal("stable supervision repeated full validation", p.checks)
	}
	p.stamp = "changed"
	if _, err := s.observeCurrent(t.Context(), id); err != nil || p.checks != 2 {
		t.Fatal(p.checks, err)
	}
	p.err = errors.New("temporary access failure")
	if _, err := s.observeCurrent(t.Context(), id); err == nil || errors.Is(err, ErrNotRunning) {
		t.Fatal("observation failure became exit", err)
	}
	p.err = nil
	reused := id
	reused.StartTicks++
	p.live[id.PID] = reused
	if _, err := s.observeCurrent(t.Context(), id); !errors.Is(err, ErrUnknown) {
		t.Fatal("PID reuse accepted", err)
	}
	// A new supervisor must validate again even if durable identity is unchanged.
	p.live[id.PID] = id
	s = &Supervisor{opts: Options{Process: p}}
	if _, err := s.observeCurrent(t.Context(), id); err != nil || p.checks != 4 {
		t.Fatal(p.checks, err)
	}
}
