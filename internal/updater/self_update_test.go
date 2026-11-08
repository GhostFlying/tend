package updater

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

type fakeSelfActions struct {
	stopped                                  bool
	stopError, startError                    error
	checkError, installError                 error
	stops, starts, installs, locks, releases int
}

func (f *fakeSelfActions) Check(_ context.Context, _ SelfUpdatePlan, phase string) error {
	if f.checkError != nil {
		return f.checkError
	}
	if phase == "stopped" && !f.stopped {
		return ErrUnknown
	}
	return nil
}
func (f *fakeSelfActions) Stop(context.Context, SelfUpdatePlan) error {
	f.stops++
	if f.stopError == nil {
		f.stopped = true
	}
	return f.stopError
}
func (f *fakeSelfActions) Install(context.Context, SelfUpdatePlan) (json.RawMessage, func(), error) {
	f.installs++
	if f.installError != nil {
		return nil, nil, f.installError
	}
	f.locks++
	released := false
	return json.RawMessage(`{"format":1}`), func() {
		if !released {
			released = true
			f.releases++
		}
	}, nil
}
func (f *fakeSelfActions) Start(context.Context, SelfUpdatePlan) error {
	f.starts++
	if f.locks != f.releases {
		return errors.New("original lock still held")
	}
	return f.startError
}
func selfEngineFixture() (SelfUpdateJournal, func() time.Time) {
	now := time.Unix(100, 0)
	j := SelfUpdateJournal{Format: 1, Phase: "prepared", Plan: SelfUpdatePlan{Request: SelfUpdateRequest{Target: protocol.UpdaterSelfTarget{RequestID: "original-request", ExpiresAtMillis: now.Add(time.Minute).UnixMilli()}}}}
	return j, func() time.Time { return now }
}
func TestSelfUpdateUnknownStopReconcilesOriginalWithoutAnotherSignal(t *testing.T) {
	j, now := selfEngineFixture()
	actions := &fakeSelfActions{stopError: ErrUnknown}
	saves := 0
	save := func() error { saves++; return nil }
	if err := advanceSelf(t.Context(), &j, save, actions, now); !errors.Is(err, ErrUnknown) || j.Phase != "stopping" {
		t.Fatal(j, err)
	}
	if err := advanceSelf(t.Context(), &j, save, actions, now); !errors.Is(err, ErrUnknown) || actions.stops != 1 || actions.starts != 0 {
		t.Fatal("stop uncertainty replayed external action", actions, err)
	}
	actions.stopped = true
	if err := advanceSelf(t.Context(), &j, save, actions, now); err != nil {
		t.Fatal(err)
	}
	if j.Phase != "starting" || actions.stops != 1 || actions.starts != 1 || actions.locks != actions.releases || len(j.OriginalState) == 0 {
		t.Fatal(j, actions, saves)
	}
	if err := advanceSelf(t.Context(), &j, save, actions, now); err != nil || actions.starts != 1 {
		t.Fatal("lost adoption caused another start", actions, err)
	}
	if j.Receipt != nil {
		t.Fatal("StartUnit fabricated adoption", j)
	}
}
func TestSelfUpdateInterruptedPhasesNeverRepeatUncertainActions(t *testing.T) {
	for _, phase := range []string{"stopping", "installing", "starting", "verified"} {
		t.Run(phase, func(t *testing.T) {
			j, now := selfEngineFixture()
			j.Phase = phase
			a := &fakeSelfActions{stopped: true, startError: ErrUnknown}
			err := advanceSelf(t.Context(), &j, func() error { return nil }, a, now)
			if phase == "stopping" || phase == "installing" {
				if !errors.Is(err, ErrUnknown) || a.starts != 1 {
					t.Fatal(a, err)
				}
			} else if err != nil || a.starts != 0 {
				t.Fatal(a, err)
			}
			a.startError = nil
			if err := advanceSelf(t.Context(), &j, func() error { return nil }, a, now); err != nil {
				t.Fatal(err)
			}
			if a.stops != 0 || a.starts > 1 {
				t.Fatal("reconciliation repeated an issued action", a)
			}
		})
	}
}
func TestSelfUpdateGuardsAndDurabilityPrecedeProcessMutation(t *testing.T) {
	for _, stage := range []string{"identity", "persist", "expiry", "lock"} {
		t.Run(stage, func(t *testing.T) {
			j, now := selfEngineFixture()
			a := &fakeSelfActions{}
			save := func() error { return nil }
			switch stage {
			case "identity":
				a.checkError = ErrUnknown
			case "persist":
				save = func() error { return errors.New("disk failure") }
			case "expiry":
				j.Plan.Request.Target.ExpiresAtMillis = now().UnixMilli()
			case "lock":
				a.installError = ErrUnknown
			}
			if err := advanceSelf(t.Context(), &j, save, a, now); err == nil {
				t.Fatal("guard failure accepted")
			}
			if a.starts != 0 || stage != "lock" && a.stops != 0 {
				t.Fatal("failed guard changed process", a)
			}
		})
	}
}
