package orca

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestFault_LaunchNotDeliveredFailsBothRolesAndPersistsAcrossRestart(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		t.Run(string(role), func(t *testing.T) {
			f := newWorkerFixture(t)
			f.orca.noState = true
			f.orca.launchPromptOutcome = promptNotDelivered
			spec := f.spec(role)
			spec.Attempt = 1
			w, err := f.ws.Start(t.Context(), spec)
			if w != nil || err == nil || !strings.Contains(err.Error(), "was not delivered") || errors.Is(err, harness.ErrWorkerNotStarted) {
				t.Fatalf("negative launch=%v %v", w, err)
			}
			var state workerState
			if err := json.Unmarshal([]byte(f.thread), &state); err != nil {
				t.Fatal(err)
			}
			if state.PromptOutcome != promptNotDelivered || state.Terminal == "" || state.Pane == "" || state.Launch != "" {
				t.Fatalf("missing negative receipt: %+v", state)
			}
			if f.orca.count("terminal wait", nil) != 0 || f.orca.count("terminal send", nil) != 0 {
				t.Fatal("waited for missing turn or resent prompt")
			}
			// A fresh worker object reads exactly the persisted JSON, including the
			// negative receipt, without acquiring a new provider operation.
			ws := NewWorkers(f.ws.opts)
			spec.Thread = f.thread
			spec.Attempt = 2
			if _, err := ws.Start(t.Context(), spec); err == nil || !strings.Contains(err.Error(), "was not delivered") {
				t.Fatalf("restored negative receipt=%v", err)
			}
			if f.orca.count("agent launch", nil) != 1 || f.orca.count("terminal send", nil) != 0 {
				t.Fatal("restart replayed undelivered launch")
			}
			if err := ws.ReleaseFinalizing(t.Context(), spec.RunID, f.thread, now(), false, func(string) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if len(f.orca.terms) != 0 || f.orca.count("terminal close", nil) != 1 {
				t.Fatal("owned empty terminal not reclaimed")
			}
		})
	}
}

func TestLaunchReceiptMustConfirmPromptBeforeTurnStarted(t *testing.T) {
	for _, outcome := range []string{promptHandedToTerminal, promptNotDelivered, promptUnconfirmed, "", "future-outcome"} {
		t.Run(outcome, func(t *testing.T) {
			var saved string
			w := &worker{ws: NewWorkers(WorkerOptions{Now: time.Now}), spec: harness.WorkerSpec{OnThread: func(thread string) error { saved = thread; return nil }}}
			var receipt Launch
			receipt.Outcome.Kind = OutcomeTerminal
			receipt.Outcome.Handle = "owned"
			receipt.Outcome.PaneKey = "tab:leaf"
			receipt.Prompt.Outcome = outcome
			err := w.launched(context.Background(), receipt, "brief")
			if outcome == promptHandedToTerminal {
				if err != nil || len(w.pending) != 1 || w.pending[0].Kind != "turn.started" {
					t.Fatalf("confirmed launch=%v %+v", err, w.pending)
				}
			} else {
				if err == nil || len(w.pending) != 0 {
					t.Fatalf("unconfirmed turn announced: %v %+v", err, w.pending)
				}
			}
			if saved == "" {
				t.Fatal("receipt was not journaled before returning")
			}
		})
	}
}

func TestFault_EmptyTerminalCleanupRetriesAndPreservesOwnerActivity(t *testing.T) {
	for _, test := range []struct {
		name                      string
		change                    func(*liveOrca, workerState)
		held, handBack, wantClose bool
	}{
		{name: "empty original", wantClose: true},
		{name: "close error then restart", change: func(f *liveOrca, _ workerState) { f.failClose = true }},
		{name: "changed pane", change: func(f *liveOrca, _ workerState) { f.terms[0].LeafID = "replacement" }},
		{name: "replacement handle", change: func(f *liveOrca, _ workerState) { f.terms[0].Handle = "owner-replacement" }},
		{name: "different workspace", change: func(f *liveOrca, _ workerState) { f.terms[0].WorktreeID = "other" }},
		{name: "owner prompt", change: func(f *liveOrca, s workerState) {
			f.agents[s.Pane] = &Agent{PaneKey: s.Pane, Prompt: "owner inspection"}
		}},
		{name: "owner working", change: func(f *liveOrca, s workerState) { f.agents[s.Pane] = &Agent{PaneKey: s.Pane, State: stateWorking} }},
		{name: "agent query failed", change: func(f *liveOrca, _ workerState) { f.failPs = true }},
		{name: "owner held", held: true},
		{name: "explicit owner handback", held: true, handBack: true, wantClose: true, change: func(f *liveOrca, s workerState) {
			f.agents[s.Pane] = &Agent{PaneKey: s.Pane, Prompt: "owner investigation", State: stateWorking}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newWorkerFixture(t)
			f.orca.noState = true
			f.orca.launchPromptOutcome = promptNotDelivered
			if _, err := f.ws.Start(t.Context(), f.spec(protocol.RoleImplement)); err == nil {
				t.Fatal("expected prompt failure")
			}
			var s workerState
			if err := json.Unmarshal([]byte(f.thread), &s); err != nil {
				t.Fatal(err)
			}
			if test.change != nil {
				test.change(f.orca, s)
			}
			err := f.ws.releaseOwned(t.Context(), "run_1", f.thread, test.held, now(), test.handBack, false)
			if test.wantClose {
				if err != nil || len(f.orca.terms) != 0 {
					t.Fatalf("close failed: %v %+v", err, f.orca.terms)
				}
			} else {
				if err == nil || len(f.orca.terms) != 1 {
					t.Fatalf("unproven/owner terminal closed: %v %+v", err, f.orca.terms)
				}
			}
			if test.name == "close error then restart" {
				f.orca.failClose = false
				ws := NewWorkers(f.ws.opts)
				if err := ws.ReleaseFinalizing(t.Context(), "run_1", f.thread, now(), false, func(string) error { return nil }); err != nil || len(f.orca.terms) != 0 {
					t.Fatalf("restart cleanup failed: %v", err)
				}
			}
		})
	}
}

func TestFault_UnknownOrOldLaunchReceiptNeverResendsOrClaimsEmptyCleanup(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "unconfirmed", true: "missing outcome"}[missing], func(t *testing.T) {
			f := newWorkerFixture(t)
			f.orca.noState = true
			f.orca.launchPromptOutcome = promptUnconfirmed
			f.orca.omitLaunchPrompt = missing
			spec := f.spec(protocol.RoleAccept)
			if _, err := f.ws.Start(t.Context(), spec); err == nil || !strings.Contains(err.Error(), "unconfirmed") {
				t.Fatalf("unknown prompt=%v", err)
			}
			saved := f.thread
			spec.Thread = saved
			if _, err := NewWorkers(f.ws.opts).Start(t.Context(), spec); err == nil {
				t.Fatal("unknown receipt replayed")
			}
			if err := f.ws.ReleaseFinalizing(t.Context(), "run_1", saved, now(), false, func(string) error { return nil }); err == nil {
				t.Fatal("unknown delivery granted empty-terminal cleanup")
			}
			if f.orca.count("agent launch", nil) != 1 || f.orca.count("terminal send", nil) != 0 || f.orca.count("terminal close", nil) != 0 || len(f.orca.terms) != 1 {
				t.Fatal("unknown operation was resent or terminal closed")
			}
		})
	}
}

func TestFault_CancelledNegativeLaunchDoesNotBypassOwnershipCleanup(t *testing.T) {
	for _, outcome := range []string{promptNotDelivered, promptUnconfirmed} {
		t.Run(outcome, func(t *testing.T) {
			f := newWorkerFixture(t)
			f.orca.noState = true
			f.orca.launchPromptOutcome = outcome
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			f.orca.onLaunch = func() error { cancel(harness.ErrStopped); return nil }
			if _, err := f.ws.Start(ctx, f.spec(protocol.RoleImplement)); err == nil {
				t.Fatal("negative launch unexpectedly started")
			}
			if f.orca.count("terminal close", nil) != 0 || f.orca.count("terminal send", nil) != 0 {
				t.Fatal("cancel raced receipt and bypassed final ownership guard")
			}
			var saved workerState
			if err := json.Unmarshal([]byte(f.thread), &saved); err != nil {
				t.Fatal(err)
			}
			if saved.PromptOutcome != outcome || saved.Terminal == "" {
				t.Fatalf("cancel lost receipt: %+v", saved)
			}
		})
	}
}

func TestFault_UndeliveredStartupStateUsesPersistedReceiptBoundary(t *testing.T) {
	for _, test := range []struct {
		name                                         string
		offset                                       int64
		updateOnly, missingReceipt, realPrompt, tool bool
		wantClose                                    bool
	}{
		{name: "startup before receipt", offset: -1, wantClose: true},
		{name: "startup equal receipt", wantClose: true},
		{name: "new working turn", offset: 1},
		{name: "later status update", offset: 1, updateOnly: true},
		{name: "old receipt without timestamp", missingReceipt: true},
		{name: "owner prompt before receipt", offset: -1, realPrompt: true},
		{name: "tool before receipt", offset: -1, tool: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newWorkerFixture(t)
			f.orca.launchPromptOutcome = promptNotDelivered
			f.orca.onLaunch = func() error { f.orca.agents["tab:1"].Prompt = ""; return nil }
			if _, err := f.ws.Start(t.Context(), f.spec(protocol.RoleImplement)); err == nil {
				t.Fatal("expected not-delivered failure")
			}
			var st workerState
			if err := json.Unmarshal([]byte(f.thread), &st); err != nil {
				t.Fatal(err)
			}
			if st.PromptReceiptAt <= 0 {
				t.Fatal("receipt timestamp was not persisted")
			}
			a := f.orca.agents[st.Pane]
			a.StateStartedAt = st.PromptReceiptAt + test.offset
			if test.updateOnly {
				a.StateStartedAt = st.PromptReceiptAt
				a.UpdatedAt = st.PromptReceiptAt + test.offset
			}
			if test.realPrompt {
				a.Prompt = "owner task"
			}
			if test.tool {
				a.ToolName = "exec_command"
			}
			if test.missingReceipt {
				st.PromptReceiptAt = 0
			}
			saved, err := json.Marshal(st)
			if err != nil {
				t.Fatal(err)
			}
			// A fresh Workers instance must use the original persisted receipt, not
			// the restart time, when distinguishing startup from later owner activity.
			opts := f.ws.opts
			opts.Now = func() time.Time { return time.UnixMilli(st.PromptReceiptAt + 10000) }
			restarted := NewWorkers(opts)
			err = restarted.ReleaseFinalizing(t.Context(), "run_1", string(saved), now(), false, func(string) error { return nil })
			if test.wantClose {
				if err != nil || len(f.orca.terms) != 0 {
					t.Fatalf("startup retained: %v", err)
				}
			} else if err == nil || len(f.orca.terms) != 1 {
				t.Fatalf("owner/unknown activity closed: %v", err)
			}
		})
	}
}

func TestFault_UnconfirmedLaunchStopsOnlyItsProvenOriginalTurn(t *testing.T) {
	fenced := errors.New("runner fenced")
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		for _, test := range []struct {
			name                string
			cause               error
			change              func(*liveOrca)
			wantSend, wantClose bool
		}{
			{name: "fenced own turn", cause: fenced, wantSend: true},
			{name: "cancelled own turn", cause: harness.ErrStopped, wantClose: true},
			{name: "held own turn", cause: harness.ErrHeld},
			{name: "owner prompt", cause: fenced, change: func(f *liveOrca) { f.agents["tab:1"].Prompt = "owner work" }},
			{name: "startup without prompt", cause: fenced, change: func(f *liveOrca) { f.agents["tab:1"].Prompt = "" }},
			{name: "owner interrupted", cause: harness.ErrStopped, change: func(f *liveOrca) { f.agents["tab:1"].Interrupted = true }},
			{name: "wrong pane", cause: fenced, change: func(f *liveOrca) { f.terms[0].LeafID = "other" }},
			{name: "wrong workspace", cause: harness.ErrStopped, change: func(f *liveOrca) { f.terms[0].WorktreeID = "other" }},
			{name: "replacement handle", cause: harness.ErrStopped, change: func(f *liveOrca) { f.terms[0].Handle = "replacement" }},
			{name: "query failure", cause: fenced, change: func(f *liveOrca) { f.failPs = true }},
		} {
			t.Run(string(role)+"/"+test.name, func(t *testing.T) {
				f := newWorkerFixture(t)
				f.orca.launchPromptOutcome = promptUnconfirmed
				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(nil)
				f.orca.onLaunch = func() error {
					if test.change != nil {
						test.change(f.orca)
					}
					cancel(test.cause)
					return nil
				}
				if _, err := f.ws.Start(ctx, f.spec(role)); !errors.Is(err, test.cause) {
					t.Fatalf("cause=%v", err)
				}
				if got := f.orca.count("terminal send", nil); (got == 1) != test.wantSend {
					t.Fatalf("sends=%d", got)
				}
				if got := f.orca.count("terminal close", nil); (got == 1) != test.wantClose {
					t.Fatalf("closes=%d", got)
				}
				var st workerState
				if err := json.Unmarshal([]byte(f.thread), &st); err != nil {
					t.Fatal(err)
				}
				if st.PromptOutcome != promptUnconfirmed {
					t.Fatal("lost uncertain receipt")
				}
				// On restart the same uncertain launch cannot be relaunched or resent.
				f.orca.onLaunch = nil
				spec := f.spec(role)
				spec.Thread = f.thread
				launches, sends := f.orca.count("agent launch", nil), f.orca.count("terminal send", nil)
				if _, err := NewWorkers(f.ws.opts).Start(t.Context(), spec); err == nil {
					t.Fatal("restart accepted uncertain launch")
				}
				if f.orca.count("agent launch", nil) != launches || f.orca.count("terminal send", nil) != sends {
					t.Fatal("restart replayed uncertain brief")
				}
			})
		}
	}
}
