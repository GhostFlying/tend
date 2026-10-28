package orca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/harness"
)

// startupState is saved before closing or replacing an undelivered terminal.
// Its allowance is independent of model-turn recovery and survives restarts.
type startupState struct {
	Attempts    int               `json:"attempts"`
	CloseIssued bool              `json:"close_issued,omitempty"`
	Stopped     bool              `json:"stopped,omitempty"`
	Phase       string            `json:"phase"`
	Terminal    string            `json:"terminal"`
	Diagnostic  *LaunchDiagnostic `json:"diagnostic,omitempty"`
}

func (w *worker) startupEvent(ctx context.Context, phase string) error {
	if w.spec.OnStartupEvent == nil {
		return nil
	}
	attempts := 0
	diagnostic := w.st.LaunchDiagnostic
	terminal := w.st.Terminal
	if s := w.st.Startup; s != nil {
		attempts = s.Attempts
		terminal = s.Terminal
		if diagnostic == nil {
			diagnostic = s.Diagnostic
		}
	}
	body, err := json.Marshal(map[string]any{"phase": phase, "attempts": attempts, "diagnostic": diagnostic, "requested_execution": harness.WorkerProfile(w.spec, w.ws.opts.Model, w.ws.opts.Effort, w.ws.opts.Acceptance), "role": w.spec.Role})
	if err != nil {
		return err
	}
	return w.spec.OnStartupEvent(ctx, harness.WorkerEvent{NativeID: fmt.Sprintf("%s/startup/%d/%s", terminal, attempts, phase), Kind: "worker.startup_" + phase, Body: body})
}

func (w *worker) beginStartup(ctx context.Context, agent string) error {
	for {
		if s := w.st.Startup; s != nil && s.Phase == "closing" {
			if err := w.startupEvent(ctx, "retry_pending"); err != nil {
				return err
			}
			if !s.Stopped {
				if s.CloseIssued {
					return errors.New("orca: startup terminal close outcome unknown; reconciliation required")
				}
				err := w.ws.releaseUndeliveredUsing(ctx, w.spec.RunID, w.st, w.spec.HandBack, false, func() error {
					s.CloseIssued = true
					if err := w.save(); err != nil {
						return err
					}
					var receipt struct {
						Close struct {
							Handle    string `json:"handle"`
							PtyKilled bool   `json:"ptyKilled"`
						} `json:"close"`
					}
					if err := w.ws.opts.Client.call(ctx, "", &receipt, "terminal", "close", "--terminal", w.st.Terminal); err != nil {
						return err
					}
					if receipt.Close.Handle != w.st.Terminal || !receipt.Close.PtyKilled {
						return errors.New("orca: startup terminal close lacks stopped process proof")
					}
					s.Stopped = true
					return w.save()
				})
				if err != nil {
					return err
				}
				// Absence without an acknowledged kill is not proof: the terminal may
				// have been detached or unregistered while its process survived.
				if !s.Stopped {
					return errors.New("orca: startup terminal disappeared without stopped process proof")
				}
			}

			terms, err := w.ws.opts.Client.Terminals(ctx, workspaceSelector(w.wt))
			if err != nil {
				return err
			}
			for _, term := range terms {
				if term.Handle == s.Terminal {
					return errors.New("orca: stopped startup terminal remains registered; reconciliation required")
				}
			}
			w.resetNative()
			w.st.Startup.Phase = "launching"
			if err := w.save(); err != nil {
				return err
			}
		}
		err := w.begin(ctx, agent)
		if err == nil {
			if w.st.Startup != nil && w.st.Startup.Phase == "launching" {
				w.st.Startup.Phase = "started"
				if err := w.save(); err != nil {
					return err
				}
			}
			// Saving the success phase and appending its event are separate durable
			// operations. Replay its stable event identity after a crash between them.
			if w.st.Startup != nil && w.st.Startup.Phase == "started" {
				if err := w.startupEvent(ctx, "started"); err != nil {
					return err
				}
			}
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
		if w.st.LaunchDiagnostic != nil {
			if eventErr := w.startupEvent(ctx, "failed"); eventErr != nil {
				return eventErr
			}
		}
		d := w.st.LaunchDiagnostic
		limit, attempts := 1, 0
		if w.ws.opts.StartupAttempts != nil {
			limit = *w.ws.opts.StartupAttempts
		}
		if w.st.Startup != nil {
			attempts = w.st.Startup.Attempts
		}
		if w.spec.HandBack || attempts >= limit || w.st.PromptOutcome != promptNotDelivered || d == nil ||
			d.Code != "readiness_timeout" || d.Phase != "readiness" || d.PromptWriteState != "none" || d.TerminalHandle != w.st.Terminal || w.st.Terminal == "" {
			return err
		}
		w.st.Startup = &startupState{Attempts: attempts + 1, Phase: "closing", Terminal: w.st.Terminal, Diagnostic: d}
		if err := w.save(); err != nil {
			return err
		}
	}
}
