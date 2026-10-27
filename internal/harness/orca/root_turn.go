package orca

import (
	"context"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/harness"
)

const rootTurnCapability = "agent-session.root-turn.v1"

// RootTurn is an execution-host observation, separate from aggregate activity.
// Unknown or missing evidence is never permission to replay a provider action.
type RootTurn struct {
	HostStartedAt int64  `json:"hostStartedAt,omitempty"`
	Version       int    `json:"version"`
	PaneKey       string `json:"paneKey"`
	LaunchID      string `json:"launchId"`
	SessionID     string `json:"sessionId"`
	TurnID        string `json:"turnId"`
	ObservedAt    int64  `json:"observedAt"`
	StartedAt     int64  `json:"startedAt"`
	CompletedAt   int64  `json:"completedAt,omitempty"`
	Source        string `json:"source"`
	State         string `json:"state"`
	Operations    string `json:"operations"`
	ErrorCode     string `json:"errorCode,omitempty"`
	Retryable     bool   `json:"retryable"`
}

func rootTurnIdentity(a Agent) bool {
	r := a.RootTurn
	return r != nil && r.Version == 1 && r.Source == "native-api" && r.PaneKey == a.PaneKey &&
		r.LaunchID != "" && r.SessionID != "" && r.TurnID != "" && a.ProviderSession != nil &&
		r.SessionID == a.ProviderSession.ID
}

func validRootTurn(a Agent, since int64) bool {
	r := a.RootTurn
	return rootTurnIdentity(a) && (r.StartedAt >= since || r.HostStartedAt >= since) && r.ObservedAt >= r.StartedAt
}

func nativeFailedTurn(a Agent, since int64) (failedTurn, error) {
	if !validRootTurn(a, since) || a.RootTurn.State != "failed" {
		return failedTurn{}, errors.New("native history does not confirm this root failure")
	}
	r := a.RootTurn
	if r.Operations != "settled" {
		return failedTurn{}, errors.New("native root operations are not confirmed settled")
	}
	if !r.Retryable {
		return failedTurn{}, errors.New("native root error is not automatically recoverable")
	}
	switch r.ErrorCode {
	case "http_connection_failed", "response_stream_connection_failed", "response_stream_disconnected", "rate_limit_exceeded":
		return failedTurn{Code: r.ErrorCode, TurnID: r.TurnID}, nil
	default:
		return failedTurn{}, errors.New("native root error is outside the recovery allowlist")
	}
}

// Probe the actual CLI-connected server, never the CLI package version.
func (c *Client) rootTurnRuntime(ctx context.Context) (string, bool, bool) {
	var status struct {
		Runtime struct {
			RuntimeID    string   `json:"runtimeId"`
			Capabilities []string `json:"capabilities"`
		} `json:"runtime"`
	}
	if c.call(ctx, "", &status, "status") != nil {
		return "", false, false
	}
	for _, capability := range status.Runtime.Capabilities {
		if capability == rootTurnCapability {
			return status.Runtime.RuntimeID, true, true
		}
	}
	return status.Runtime.RuntimeID, false, status.Runtime.RuntimeID != ""
}

// Observation transitions are journaled before publishing; restart replays the
// same event identity and cannot lose a resolution or reset reminder dedup.
func (w *worker) recordRootObservation(ctx context.Context, a Agent) error {
	r := a.RootTurn
	if r == nil {
		if !a.legacyRootSupport || w.st.RootObservationPhase == "" {
			return nil
		}
		identity := fmt.Sprintf("legacy/%s/%d", w.st.SessionID, w.st.Turn)
		if w.st.RootObservationPhase != "known" || w.st.RootObservationIdentity != identity {
			w.st.RootObservationPhase, w.st.RootObservationIdentity = "known", identity
			w.st.RootObservationRevision++
			if err := w.save(); err != nil {
				return err
			}
		}
		if w.observationEmitted != w.st.RootObservationRevision {
			w.observationEmitted = w.st.RootObservationRevision
			w.emitID(ctx, fmt.Sprintf("%s/root-observation/%d", identity, w.st.RootObservationRevision), "worker.root_observation_resolved", map[string]any{"managed_turn_at": w.st.Turn})
		}
		return nil
	}
	phase := "known"
	code := r.ErrorCode
	rootState := r.State
	ended := r.State == "failed" || r.State == "completed" || r.State == "cancelled"
	if !validRootTurn(a, w.st.Turn) {
		phase, rootState = "unknown", "unknown"
		code = "observation_unavailable"
		if w.ws.opts.Now().UnixMilli()-w.st.Turn < 10000 {
			code = "observation_pending"
		}
	} else if r.State == "unknown" && r.ErrorCode == "ending_unconfirmed" && a.MainAgent != nil && a.MainAgent.State == stateWorking && a.MainAgent.StateStartedAt >= w.st.Turn && !rootFailed(a, w.st.Turn) {
		// A readable turn without an end clock is expected while its TUI is working.
		phase = "known"
	} else if r.State == "unknown" && r.ErrorCode == "ending_unconfirmed" && a.MainAgent != nil && a.MainAgent.State == stateDone && !rootFailed(a, w.st.Turn) && a.MainAgent.StateStartedAt >= w.st.Turn && w.ws.opts.Now().UnixMilli()-a.MainAgent.StateStartedAt < 10000 {
		phase, code = "unknown", "observation_pending"
	} else if r.State == "unknown" || ended && r.Operations == "unknown" || rootFailed(a, w.st.Turn) && !ended && r.Operations != "settled" {
		phase = "unknown"
	} else if ended && r.Operations == "pending" {
		phase = "pending"
	}
	identity := r.SessionID + "/" + r.TurnID
	if phase == "unknown" {
		identity += "/" + code
	}
	if phase != w.st.RootObservationPhase || identity != w.st.RootObservationIdentity {
		w.st.RootObservationPhase, w.st.RootObservationIdentity = phase, identity
		w.st.RootObservationRevision++
		if err := w.save(); err != nil {
			return err
		}
	}
	if w.observationEmitted == w.st.RootObservationRevision {
		return nil
	}
	w.observationEmitted = w.st.RootObservationRevision
	kind := "worker.root_observation_resolved"
	if phase == "unknown" {
		kind = "worker.root_observation_unknown"
	} else if phase == "pending" {
		kind = "worker.root_background_pending"
	}
	if rootFailed(a, w.st.Turn) {
		rootState = "failed"
	}
	w.emitID(ctx, fmt.Sprintf("%s/root-observation/%d", identity, w.st.RootObservationRevision), kind, map[string]any{"turn_at": w.st.Turn, "managed_turn_at": w.st.Turn, "background_pending": phase != "known", "root_state": rootState, "code": code, "root_turn": r})
	return nil
}

// Once native evidence has been observed, omission cannot downgrade the run to
// legacy completion or recovery. Handback authorizes one durable rebind to the
// adopted terminal/session, including observations that arrive after handback.
func (w *worker) bindRootObservation(a *Agent) error {
	changed := false
	if a.rootProvisional && !w.st.RootObservationRequired && a.RuntimeID != "" && a.RuntimeID == w.st.RootLegacyRuntimeID {
		a.RootTurn = nil
		a.legacyRootSupport = true
	}
	if a.legacyRootSupport && !w.st.RootObservationRequired && a.RuntimeID != "" && w.st.RootLegacyRuntimeID != a.RuntimeID {
		w.st.RootLegacyRuntimeID = a.RuntimeID
		changed = true
	}
	if a.RootTurn != nil && a.RootTurn.TurnID == "unobserved" && w.st.RootLastObservationTurnID != "" {
		a.RootTurn.TurnID = w.st.RootLastObservationTurnID
	}
	if a.RootTurn != nil && a.RootTurn.ErrorCode == "observation_pending" && (w.st.RootLastObservationTurnID != "" || w.ws.opts.Now().UnixMilli()-w.st.Turn >= 10000) {
		a.RootTurn.ErrorCode = "observation_unavailable"
	}
	if a.RootTurn != nil && !a.rootProvisional && !w.st.RootObservationRequired {
		w.st.RootObservationRequired = true
		changed = true
	}
	if w.spec.HandBack && !w.st.RootRebindPending {
		w.st.RootRebindPending = true
		w.st.RootRuntimeID, w.st.RootLaunchID = "", ""
		w.st.RootObservedTurnID, w.st.RootObservedForTurn = "", 0
		w.st.RootLastObservationTurnID = ""
		if a.ProviderSession != nil {
			w.st.RootRebindSessionID = a.ProviderSession.ID
		} else {
			w.st.RootRebindSessionID = ""
		}
		changed = true
	}
	if w.st.RootRebindPending && a.legacyRootSupport {
		w.st.RootObservationRequired = false
		w.st.RootRebindPending = false
		w.st.RootLegacyRuntimeID = a.RuntimeID
		changed = true
	}
	if w.st.RootRebindPending && w.st.RootRebindSessionID != "" && a.ProviderSession != nil && a.ProviderSession.ID != w.st.RootRebindSessionID {
		return fmt.Errorf("%w: handed-back provider session changed before observation", harness.ErrHeld)
	}
	if !w.st.RootRebindPending && w.st.RootRuntimeID != "" && a.RuntimeID != "" && a.RuntimeID != w.st.RootRuntimeID {
		return fmt.Errorf("%w: native runtime changed", harness.ErrHeld)
	}
	if r := a.RootTurn; r != nil {
		if !w.st.RootRebindPending && w.st.RootLaunchID != "" && r.LaunchID != "" && r.LaunchID != w.st.RootLaunchID {
			return fmt.Errorf("%w: native terminal launch changed", harness.ErrHeld)
		}
		if rootTurnIdentity(*a) && w.st.RootLastObservationTurnID != r.TurnID {
			w.st.RootLastObservationTurnID = r.TurnID
			changed = true
		}
		if validRootTurn(*a, w.st.Turn) {
			if !w.st.RootRebindPending && w.st.RootObservedForTurn == w.st.Turn && w.st.RootObservedTurnID != "" && r.TurnID != w.st.RootObservedTurnID {
				return fmt.Errorf("%w: native turn changed without a managed send", harness.ErrHeld)
			}
			if w.st.RootObservedTurnID != r.TurnID || w.st.RootObservedForTurn != w.st.Turn {
				w.st.RootObservedTurnID, w.st.RootObservedForTurn = r.TurnID, w.st.Turn
				changed = true
			}
		}
		if rootTurnIdentity(*a) && (w.st.RootLaunchID == "" || w.st.RootRebindPending) {
			w.st.RootLaunchID, w.st.RootRuntimeID = r.LaunchID, a.RuntimeID
			w.st.RootRebindPending = false
			changed = true
		}
	} else if w.st.RootObservationRequired || w.st.RootLaunchID != "" {
		sessionID := w.st.SessionID
		if a.ProviderSession != nil {
			sessionID = a.ProviderSession.ID
		}
		a.RootTurn = &RootTurn{Version: 1, Source: "native-api", PaneKey: a.PaneKey, LaunchID: w.st.RootLaunchID,
			SessionID: sessionID, TurnID: w.st.RootLastObservationTurnID, State: "unknown", Operations: "unknown", ErrorCode: "observation_unavailable"}
	}
	if changed {
		return w.save()
	}
	return nil
}
