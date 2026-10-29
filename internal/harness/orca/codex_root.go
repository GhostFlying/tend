package orca

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

const transcriptRootSource = "native-transcript"

// codexRoot reads the session already located by Orca. A pane's latest prompt
// can belong to an ephemeral thread; only this session's recorded managed input
// establishes the launch and current turn. No provider action is performed.
func codexRoot(ctx context.Context, session ProviderSession, st workerState, runID string) (Agent, error) {
	f, err := os.Open(session.TranscriptPath)
	if err != nil {
		return Agent{}, errors.New("orca: root transcript is unavailable")
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	identity, prompt, turn, managedTurn, answer := "", "", "", "", ""
	// Explicit handback may adopt a replacement session. The saved rebind
	// receipt names that session; the new managed send still needs native proof.
	launch := st.RootRebindSessionID == session.ID && st.SessionID == session.ID && session.ID != ""
	started, ended, updated := int64(0), int64(0), int64(0)
	state, outcome, code := "unknown", "", ""
	pending := map[string]bool{}
	operations := newCodexOperations()
	owned := launch
	marker := "[tend run " + runID + ", launch " + st.Marker + "]"
	for {
		if err := ctx.Err(); err != nil {
			return Agent{}, err
		}
		var row struct {
			Type      string    `json:"type"`
			Timestamp time.Time `json:"timestamp"`
			Payload   struct {
				Type             string             `json:"type"`
				ID               string             `json:"id"`
				TurnID           string             `json:"turn_id"`
				Role             string             `json:"role"`
				CallID           string             `json:"call_id"`
				Name             string             `json:"name"`
				Arguments        json.RawMessage    `json:"arguments"`
				Output           json.RawMessage    `json:"output"`
				Item             codexCompletedItem `json:"item"`
				LastAgentMessage string             `json:"last_agent_message"`
				Metadata         *struct {
					ContentItemKinds []string `json:"content_item_kinds"`
				} `json:"internal_chat_message_metadata_passthrough"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				Error *struct {
					Code           string          `json:"code"`
					Message        string          `json:"message"`
					CodexErrorInfo json.RawMessage `json:"codex_error_info"`
				} `json:"error"`
			} `json:"payload"`
		}
		if err := decoder.Decode(&row); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return Agent{}, errors.New("orca: root transcript has an incomplete record")
		}
		p := row.Payload
		if row.Type == "session_meta" {
			identity = p.ID
			continue
		}
		stamp := row.Timestamp.UnixMilli()
		if row.Type == "event_msg" {
			switch p.Type {
			case "task_started", "turn_started":
				turn, started, ended, prompt, answer = p.TurnID, stamp, 0, "", ""
				state, outcome, code = "running", "", ""
			case "task_complete", "turn_complete":
				if p.TurnID == "" || p.TurnID != turn {
					continue
				}
				state, outcome, ended = "completed", "success", stamp
				if p.LastAgentMessage != "" {
					answer = p.LastAgentMessage
				}
				if p.Error != nil {
					state, outcome = "failed", "failure"
					code = classifyRootError(p.Error.Code, p.Error.Message, p.Error.CodexErrorInfo)
				}
			case "item_completed":
				if owned {
					operations.completed(p.Item, session.ID)
				}
			case "turn_aborted":
				if p.TurnID != "" && p.TurnID != turn {
					continue
				}
				state, outcome, ended = "cancelled", "cancellation", stamp
			}
		}
		if row.Type == "response_item" {
			switch p.Type {
			case "function_call", "custom_tool_call":
				if owned && p.CallID != "" {
					pending[p.CallID] = true
					operations.call(p.CallID, p.Name, p.Arguments)
				}
			case "function_call_output", "custom_tool_call_output":
				if owned {
					operations.output(p.CallID, p.Output)
					delete(pending, p.CallID)
				}
			case "message":
				if p.Role == "assistant" {
					var text []string
					for _, content := range p.Content {
						if content.Type == "output_text" || content.Type == "text" {
							text = append(text, content.Text)
						}
					}
					if len(text) != 0 {
						answer = strings.Join(text, "\n")
					}
				}
				userInput := true
				if p.Metadata != nil {
					userInput = codexUserInput(p.Metadata.ContentItemKinds, len(p.Content))
				}
				if p.Role == "user" && userInput {
					var text []string
					for _, content := range p.Content {
						if content.Type == "input_text" || content.Type == "text" {
							text = append(text, content.Text)
						}
					}
					input := said(strings.Join(text, "\n"))
					if st.Marker != "" && strings.HasPrefix(input, marker) && started >= st.Asked && turn != "" {
						launch, owned = true, true
					}
					// Startup instructions precede the managed launch. Once bound,
					// subsequent actual user records remain visible to ownership checks.
					if launch && input != "" {
						prompt = input
						if managedTurn == "" && started >= st.Turn && input == st.Said {
							managedTurn, owned = turn, true
						}
					}
				}
			}
		}
		updated = max(updated, stamp)
	}
	if identity == "" || identity != session.ID || !launch || turn == "" || prompt == "" {
		return Agent{}, errors.New("orca: root transcript does not confirm the managed launch and current turn")
	}
	// A saved send can precede its native turn. Preserve the proven original
	// root identity/input for receipt reconciliation, without treating it as the
	// new managed turn: validRootTurn still requires the new start boundary.
	if started >= st.Turn && (managedTurn == "" || managedTurn != turn || prompt != st.Said) {
		return Agent{}, errors.Join(harness.ErrHeld, errors.New("orca: native session has input outside the managed turn"))
	}
	operationState := "unknown"
	if ended != 0 {
		operationState = operations.state()
		if len(pending) != 0 {
			operationState = "pending"
		}
	}
	a := Agent{PaneKey: st.Pane, ProviderSession: &session, Prompt: prompt, State: stateWorking, StateStartedAt: started, UpdatedAt: updated, LastAssistantMessage: answer}
	a.RootTurn = &RootTurn{Version: 1, Source: transcriptRootSource, PaneKey: st.Pane, LaunchID: st.Marker, SessionID: identity, TurnID: turn, StartedAt: started, CompletedAt: ended, ObservedAt: updated, State: state, Operations: operationState, ErrorCode: code, Retryable: recoverableRootCode(code)}
	if ended != 0 {
		a.State = stateDone
		a.MainAgent = &MainAgent{State: stateDone, Outcome: outcome, StateStartedAt: ended}
		a.Interrupted = state == "cancelled"
	}
	return a, nil
}

func (w *worker) observeCodexRoot(ctx context.Context, a *Agent) error {
	if w.ws.opts.Agent != protocol.AgentCodex || a.RootTurn != nil {
		return nil
	}
	expectedRuntime := w.st.RootRuntimeID
	if expectedRuntime == "" {
		expectedRuntime = w.st.RootLegacyRuntimeID
	}
	rebinding := w.spec.HandBack || w.st.RootRebindPending
	if !rebinding && a.RuntimeID != "" && expectedRuntime != "" && a.RuntimeID != expectedRuntime {
		return errors.Join(harness.ErrHeld, errors.New("orca: managed root runtime changed"))
	}
	if !rebinding && w.st.SessionID != "" && a.ProviderSession != nil && a.ProviderSession.ID != w.st.SessionID {
		return errors.Join(harness.ErrHeld, errors.New("orca: managed provider session changed"))
	}
	var native Agent
	var err error
	if w.spec.HandBack || a.RuntimeID == "" || a.ProviderSession == nil || a.ProviderSession.ID == "" {
		err = errors.New("root identity is not observed")
	} else {
		native, err = codexRoot(ctx, *a.ProviderSession, w.st, w.spec.RunID)
	}
	if errors.Is(err, harness.ErrHeld) {
		return err
	}
	if err != nil {
		// An unbound or partially written session is not a completed turn or
		// evidence of owner input. Keep the original bridge and execution alive.
		a.Prompt, a.State, a.MainAgent, a.Interrupted = "", stateWorking, nil, false
		a.RootTurn = &RootTurn{Version: 1, Source: transcriptRootSource, PaneKey: w.st.Pane, State: "unknown", Operations: "unknown", ErrorCode: "observation_pending"}
		return nil
	}
	if w.st.SessionID == "" || w.st.RootRebindPending {
		w.st.SessionID = native.ProviderSession.ID
		w.st.RootLegacyRuntimeID = a.RuntimeID
		if err := w.save(); err != nil {
			return err
		}
	}
	native.RuntimeID, native.AgentType = a.RuntimeID, a.AgentType
	native.Children, native.SubagentObservation = a.Children, a.SubagentObservation
	if a.SubagentObservation == "live" {
		for _, child := range a.Children {
			if child.State != "done" || child.Membership != "settled" {
				// A returned tool call and root end do not prove its known
				// external child has stopped. Keep that explicit live fact.
				native.RootTurn.Operations = "pending"
				break
			}
		}
	}
	// The root answer also comes from the original session. Result/verdict
	// tools remain authoritative outputs; aggregate background text is not one.
	*a = native
	return nil
}

func recoverableRootCode(code string) bool {
	switch code {
	case "http_connection_failed", "response_stream_connection_failed", "response_stream_disconnected", "rate_limit_exceeded":
		return true
	default:
		return false
	}
}

// Host classifications align with content entries. Missing, incomplete and
// unknown metadata remain actual user input; only fully annotated context is
// excluded. No text marker can confer this classification.
func codexUserInput(kinds []string, contentCount int) bool {
	if len(kinds) == 0 || len(kinds) != contentCount {
		return true
	}
	for _, kind := range kinds {
		if strings.HasPrefix(kind, "user.") {
			return true
		}
		switch kind {
		case "", "unknown", "images.preparation_error", "images.unsupported", "audio.unsupported":
			return true
		}
	}
	return false
}
