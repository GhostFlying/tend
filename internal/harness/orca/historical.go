package orca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/harness"
)

// ReleaseHistorical closes only an idle agent still answering tend's recorded turn.
func (ws *Workers) ReleaseHistorical(ctx context.Context, runID, thread string, held bool, completedAt int64) error {
	return ws.releaseOwned(ctx, runID, thread, held, completedAt, false, true)
}

// ReleaseCompleted accepts an explicit owner handback without repeating the task.
func (ws *Workers) ReleaseCompleted(ctx context.Context, runID, thread string, completedAt int64, handBack bool) error {
	return ws.releaseOwned(ctx, runID, thread, false, completedAt, handBack, true)
}

// ReleaseFinalizing permits stopping the original failed turn, including an
// active one, but refuses activity newer than the persisted final intent.
func (ws *Workers) ReleaseFinalizing(ctx context.Context, runID, thread string, intentAt int64, handBack bool, saveThread func(string) error) error {
	var st workerState
	if err := json.Unmarshal([]byte(thread), &st); err != nil {
		return fmt.Errorf("orca: decode finalizing worker: %w", err)
	}
	// A refused launch explicitly clears its saved state before returning.
	if st == (workerState{}) {
		return nil
	}
	if st.Terminal == "" && st.Marker != "" && st.Worktree != "" {
		handle, pane, err := ws.byMarker(ctx, st.Worktree, st.Marker)
		if err != nil {
			return err
		}
		if handle == "" {
			return errors.New("orca: launch outcome is unknown; no stopped process proof")
		}
		st.Terminal, st.Pane = handle, pane
	}
	// Finalization never replays an uncertain launch or prompt. It inspects the
	// exact resolved terminal and expected prompt before deciding whether to stop.
	st.Launch, st.Send, st.SendText = "", "", ""
	body, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if string(body) != thread {
		if err := saveThread(string(body)); err != nil {
			return err
		}
	}
	return ws.releaseOwned(ctx, runID, string(body), false, intentAt, handBack, false)
}

func (ws *Workers) releaseOwned(ctx context.Context, runID, thread string, held bool, completedAt int64, handBack, requireDone bool) error {
	var st workerState
	if json.Unmarshal([]byte(thread), &st) != nil || st.Worktree == "" || st.Terminal == "" || st.Pane == "" {
		return errors.New("orca: historical agent identity is incomplete; manual reconciliation required")
	}
	if st.Launch != "" || st.Send != "" {
		return errors.New("orca: historical provider mutation is unresolved; manual reconciliation required")
	}
	terms, err := ws.opts.Client.Terminals(ctx, workspaceSelector(st.Worktree))
	if err != nil {
		return err
	}
	found := false
	for _, t := range terms {
		if t.PaneKey() == st.Pane && t.Handle != st.Terminal {
			return fmt.Errorf("%w: the saved pane now belongs to another terminal", harness.ErrHeld)
		}
		if t.Handle == st.Terminal {
			if t.PaneKey() != st.Pane || t.WorktreeID != st.Worktree {
				return errors.New("orca: historical terminal identity changed")
			}
			found = true
		}
	}
	if !found {
		return nil
	}
	if held {
		return fmt.Errorf("%w: the terminal was handed to its owner", harness.ErrHeld)
	}
	if handBack {
		return ws.Release(ctx, runID, thread)
	}
	agents, err := ws.opts.Client.Agents(ctx, st.Worktree)
	if err != nil {
		return err
	}
	for _, a := range agents {
		if a.PaneKey == st.Pane {
			if completedAt <= 0 {
				return fmt.Errorf("%w: completed turn timestamp is unknown; explicit handback required", harness.ErrHeld)
			}
			if a.StateStartedAt > completedAt || a.UpdatedAt > completedAt || a.Prompt != "" && st.Said != "" && !samePrompt(said(a.Prompt), st.Said) || a.Interrupted != st.Interrupted {
				return fmt.Errorf("%w: the terminal has activity outside the completed tend turn", harness.ErrHeld)
			}
			if requireDone && (!answered(a, st.Turn) || a.State != stateDone) || a.Prompt == "" || st.Said == "" || !samePrompt(said(a.Prompt), st.Said) {
				return errors.New("orca: historical terminal has manual, active or unknown activity; leave it to its owner")
			}
			return ws.Release(ctx, runID, thread)
		}
	}
	return errors.New("orca: historical terminal agent state is unknown; manual reconciliation required")
}
