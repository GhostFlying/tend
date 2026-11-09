package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

// workerOutcome is a local observation, independent of the close receipt and
// upload queue. An acknowledged native event must not erase a failed turn.
type workerOutcome struct {
	Version  int    `json:"version"`
	RunID    string `json:"run_id"`
	ThreadID string `json:"thread_id"`
	TurnID   string `json:"turn_id,omitempty"`
	Attempt  int    `json:"attempt"`
	Status   string `json:"status"`
	Source   string `json:"source"`
	Error    string `json:"error,omitempty"`
}

const maxWorkerOutcomeBytes = 64 << 10

func workerOutcomePath(dir string, attempt int) string {
	return filepath.Join(dir, fmt.Sprintf("worker-outcome-%d.json", attempt))
}

func readWorkerOutcome(dir, run, thread string, attempt int) (*workerOutcome, error) {
	f, err := os.Open(workerOutcomePath(dir, attempt))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, maxWorkerOutcomeBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxWorkerOutcomeBytes {
		return nil, errors.New("runner: original worker outcome exceeds its bound")
	}
	var outcome workerOutcome
	if err := json.Unmarshal(body, &outcome); err != nil {
		return nil, fmt.Errorf("runner: decode original worker outcome: %w", err)
	}
	if outcome.Version != 1 || outcome.RunID != run || outcome.ThreadID != thread || outcome.Attempt != attempt {
		return nil, errors.New("runner: original worker outcome identity changed")
	}
	if outcome.Status != "completed" && outcome.Status != "failed" && outcome.Status != "interrupted" && outcome.Status != "unknown" {
		return nil, errors.New("runner: original worker outcome status is invalid")
	}
	if outcome.Source != "native_terminal" && outcome.Source != "worker_wait" && outcome.Source != "run_control" {
		return nil, errors.New("runner: original worker outcome source is invalid")
	}
	if outcome.Source == "native_terminal" && outcome.TurnID == "" {
		return nil, errors.New("runner: original native outcome lacks its turn")
	}
	return &outcome, nil
}

func saveWorkerOutcome(ctx context.Context, dir string, outcome workerOutcome) (*workerOutcome, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if dir == "" || outcome.ThreadID == "" || outcome.Attempt < 1 {
		return nil, errors.New("runner: original worker outcome has no execution binding")
	}
	prior, err := readWorkerOutcome(dir, outcome.RunID, outcome.ThreadID, outcome.Attempt)
	if err != nil {
		return nil, err
	}
	if prior != nil && prior.Source == "native_terminal" {
		if outcome.Source == "native_terminal" {
			if prior.TurnID == outcome.TurnID && prior.Status != outcome.Status {
				return nil, errors.New("runner: original native terminal observations conflict")
			}
			// Events are consumed serially. A later turn in the same attempt
			// replaces the earlier turn's outcome, including a repair turn.
		} else {
			// Wait or later control cancellation cannot replace an already
			// observed native terminal. Current control is checked separately.
			return prior, nil
		}
	}
	body, err := json.Marshal(outcome)
	if err != nil {
		return nil, err
	}
	if len(body) > maxWorkerOutcomeBytes {
		return nil, errors.New("runner: original worker outcome exceeds its bound")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := replaceUploadState(workerOutcomePath(dir, outcome.Attempt), func(f *os.File) error {
		_, err := f.Write(body)
		return err
	}); err != nil {
		return nil, fmt.Errorf("runner: persist original worker outcome: %w", err)
	}
	return &outcome, nil
}

func nativeWorkerOutcome(run, thread string, attempt int, event harness.WorkerEvent) (*workerOutcome, error) {
	if event.Kind != "native.turn_completed" {
		return nil, nil
	}
	var body struct {
		Thread string `json:"thread_id"`
		Turn   string `json:"turn_id"`
		Data   struct {
			Turn struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turn"`
		} `json:"data"`
	}
	if err := json.Unmarshal(event.Body, &body); err != nil {
		return nil, err
	}
	if body.Thread != thread || body.Turn == "" || body.Data.Turn.ID != body.Turn {
		return nil, errors.New("runner: original native terminal identity differs")
	}
	status := body.Data.Turn.Status
	if status != "completed" && status != "failed" && status != "interrupted" {
		status = "unknown"
	}
	return &workerOutcome{Version: 1, RunID: run, ThreadID: thread, TurnID: body.Turn, Attempt: attempt, Status: status, Source: "native_terminal"}, nil
}

func (r *Runner) restoreWorkerOutcome(ctx context.Context, as protocol.Assignment, run journal.Run, p Prepared) (*workerOutcome, error) {
	outcome, err := readWorkerOutcome(p.StateDir, as.RunID, run.Thread, run.Attempts)
	if err != nil || outcome != nil {
		return outcome, err
	}
	// Old runs did not have an outcome file. A retained original terminal
	// event can supply that fact once; future recovery no longer needs the
	// event after its normal acknowledgement. Multiple old attempts without
	// an attempt-bound receipt cannot borrow another attempt's terminal.
	if run.Attempts != 1 {
		return nil, nil
	}
	event, ok, err := r.cfg.Journal.LatestNativeTerminal(ctx, as.RunID, run.Thread)
	if err != nil || !ok {
		return nil, err
	}
	outcome, err = nativeWorkerOutcome(as.RunID, run.Thread, run.Attempts, harness.WorkerEvent{Kind: event.Kind, Body: event.Body})
	if err != nil {
		return nil, err
	}
	return saveWorkerOutcome(ctx, p.StateDir, *outcome)
}
