package orca

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
)

func TestHistoricalReleasePreservesManualOrUnknownActivity(t *testing.T) {
	for _, tc := range []struct {
		name, prompt, state string
		held, missingState  bool
		wantStop            bool
		updatedAt           int64
	}{
		{name: "original idle turn", prompt: "tend brief", state: stateDone, wantStop: true},
		{name: "manual repeats tend prompt", prompt: "tend brief", state: stateDone, updatedAt: 300},
		{name: "manual prompt", prompt: "owner investigation", state: stateDone},
		{name: "still working", prompt: "tend brief", state: stateWorking},
		{name: "unknown state", missingState: true},
		{name: "owner holds original turn", prompt: "tend brief", state: stateDone, held: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLiveOrca()
			term := Terminal{Handle: "term_1", WorktreeID: f.wt, TabID: "tab", LeafID: "1"}
			f.terms = []Terminal{term}
			if !tc.missingState {
				f.agents[term.PaneKey()] = &Agent{PaneKey: term.PaneKey(), Prompt: tc.prompt, State: tc.state, StateStartedAt: 100, UpdatedAt: tc.updatedAt}
			}
			st, _ := json.Marshal(workerState{Terminal: term.Handle, Pane: term.PaneKey(), Worktree: f.wt, Turn: 100, Said: "tend brief"})
			ws := NewWorkers(WorkerOptions{Client: &Client{Exec: f.exec}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
			err := ws.ReleaseHistorical(context.Background(), "run_1", string(st), tc.held, 200)
			if tc.wantStop {
				if err != nil || len(f.terms) != 0 {
					t.Fatalf("release = %v, terminals=%d", err, len(f.terms))
				}
			} else if err == nil || len(f.terms) != 1 {
				t.Fatalf("manual terminal changed: err=%v terminals=%d", err, len(f.terms))
			}
		})
	}
}

func TestHistoricalHeldTerminalCanBeConfirmedAfterOwnerClosesIt(t *testing.T) {
	f := newLiveOrca()
	st, _ := json.Marshal(workerState{Terminal: "closed", Pane: "tab:1", Worktree: f.wt, Turn: 100, Said: "tend brief"})
	ws := NewWorkers(WorkerOptions{Client: &Client{Exec: f.exec}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := ws.ReleaseHistorical(context.Background(), "run_1", string(st), true, 200); err != nil {
		t.Fatal(err)
	}
}

func TestCompletedRecoveryDoesNotAdoptReplacementTerminalInSavedPane(t *testing.T) {
	f := newLiveOrca()
	f.terms = []Terminal{{Handle: "owner-replacement", WorktreeID: f.wt, TabID: "tab", LeafID: "1"}}
	st, _ := json.Marshal(workerState{Terminal: "closed-original", Pane: "tab:1", Worktree: f.wt, Turn: 100, Said: "tend brief"})
	ws := NewWorkers(WorkerOptions{Client: &Client{Exec: f.exec}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := ws.ReleaseCompleted(context.Background(), "run_1", string(st), 200, false); err == nil {
		t.Fatal("replacement pane was treated as an empty stopped resource")
	}
	if len(f.terms) != 1 || f.terms[0].Handle != "owner-replacement" {
		t.Fatal("replacement terminal was closed")
	}
}

func TestFinalizingReleaseUsesImmutableIntentForActiveFailedTurn(t *testing.T) {
	for _, tc := range []struct {
		name, prompt       string
		updatedAt          int64
		handBack, wantStop bool
	}{
		{name: "original active failed turn", prompt: "tend brief", wantStop: true},
		{name: "manual new prompt", prompt: "owner investigation"},
		{name: "manual identical prompt after intent", prompt: "tend brief", updatedAt: 300},
		{name: "explicit handback of manual turn", prompt: "owner investigation", updatedAt: 300, handBack: true, wantStop: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLiveOrca()
			term := Terminal{Handle: "term_failure", WorktreeID: f.wt, TabID: "tab", LeafID: "1"}
			f.terms = []Terminal{term}
			f.agents[term.PaneKey()] = &Agent{PaneKey: term.PaneKey(), Prompt: tc.prompt, State: stateWorking, StateStartedAt: 100, UpdatedAt: tc.updatedAt}
			st, _ := json.Marshal(workerState{Terminal: term.Handle, Pane: term.PaneKey(), Worktree: f.wt, Turn: 100, Said: "tend brief"})
			ws := NewWorkers(WorkerOptions{Client: &Client{Exec: f.exec}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
			err := ws.ReleaseFinalizing(context.Background(), "run_failure", string(st), 200, tc.handBack, func(string) error { return nil })
			if tc.wantStop {
				if err != nil || len(f.terms) != 0 {
					t.Fatalf("original failed turn not stopped: %v terminals=%d", err, len(f.terms))
				}
			} else if err == nil || len(f.terms) != 1 {
				t.Fatalf("manual terminal changed: %v terminals=%d", err, len(f.terms))
			}
		})
	}
}
