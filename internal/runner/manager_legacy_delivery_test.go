package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestLegacyManagerRetriesOriginalDelivery(t *testing.T) {
	const budget = 100 * time.Millisecond
	for _, tc := range []struct {
		name        string
		operation   string
		outage      time.Duration
		wantTimeout bool
	}{
		{name: "event outlasts request budget", operation: protocol.OpManagerNativeEvent, outage: budget},
		{name: "reply recovers within budget", operation: protocol.OpManagerNativeReply, outage: 20 * time.Millisecond},
		{name: "reply remains pending after budget", operation: protocol.OpManagerNativeReply, outage: 10 * budget, wantTimeout: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			operation := tc.operation
			j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), time.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			synctest.Test(t, func(t *testing.T) {
				ctx := t.Context()
				as := protocol.Assignment{RunID: "original-run", RunToken: "original-token", LeaseID: "original-lease", Role: protocol.RoleIssueManager, Protocol: protocol.MinimumRunnerProtocol}
				if err := j.AcceptAssignment(ctx, as, ""); err != nil {
					t.Fatal(err)
				}
				first := protocol.Envelope{RequestID: "original-first", Operation: operation, Target: runTarget(as), Payload: json.RawMessage(`{"id":"original-command","result":{}}`)}
				expected := []protocol.Envelope{first}
				if operation == protocol.OpManagerNativeEvent {
					second := first
					second.RequestID = "original-second"
					expected = append(expected, second)
					for i := range expected {
						if err := j.SaveManagerEvent(ctx, as.RunID, &expected[i], json.RawMessage(`{"thread_id":"original-thread"}`)); err != nil {
							t.Fatal(err)
						}
					}
				} else if err := j.PutRequest(ctx, journal.Request{RunID: as.RunID, Envelope: first}); err != nil {
					t.Fatal(err)
				}
				started := time.Now()
				var delivered []string
				acknowledged, reportedUnknown := false, false
				client := &http.Client{Transport: uploadRoundTripper(func(req *http.Request) (*http.Response, error) {
					defer req.Body.Close()
					payload, err := io.ReadAll(req.Body)
					if err != nil {
						return nil, err
					}
					var env protocol.Envelope
					if err := json.Unmarshal(payload, &env); err != nil {
						return nil, err
					}
					if env.Operation == protocol.OpManagerNativeClaim {
						var claim protocol.ManagerNativeClaimPayload
						if err := json.Unmarshal(env.Payload, &claim); err != nil || !claim.AcknowledgeOnly ||
							!slices.Equal(claim.AcknowledgedIDs, []string{"original-command"}) || len(delivered) != 1 || acknowledged {
							t.Error("expected an acknowledgement of the single delivered native reply", claim, err)
						}
						acknowledged = true
						return managerReceiptHTTPResponse(http.StatusOK, rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
					}
					index := len(delivered)
					if index >= len(expected) {
						t.Error("delivery ran again after acknowledgement")
						return nil, errors.New("unexpected request")
					}
					want, err := json.Marshal(expected[index])
					if err != nil {
						return nil, err
					}
					if !bytes.Equal(payload, want) || req.Header.Get(headerRun) != as.RunID {
						t.Error("retry changed the original envelope, Run or order")
						return managerReceiptHTTPResponse(http.StatusConflict, rpcResponse{Error: protocol.Errorf(protocol.CodeConflictPayload, "changed original")})
					}
					if time.Since(started) <= tc.outage {
						return nil, errors.New("connection temporarily lost")
					}
					if !reportedUnknown {
						reportedUnknown = true
						return managerReceiptHTTPResponse(http.StatusOK, rpcResponse{Operation: &protocol.Operation{RequestID: expected[index].RequestID, Status: protocol.OpUnknown}})
					}
					delivered = append(delivered, expected[index].RequestID)
					return managerReceiptHTTPResponse(http.StatusOK, rpcResponse{Operation: &protocol.Operation{RequestID: expected[index].RequestID, Status: protocol.OpSucceeded}})
				})}
				r := New(Config{Journal: j, IDs: testkit.NewIDGen(testkit.NewClock(), 1), ServerURL: "http://runner.invalid", HTTPClient: client, Now: time.Now, RetryDelay: 10 * time.Millisecond, RequestTimeout: budget, Log: slog.New(slog.DiscardHandler)})
				var got error
				want := []string{first.RequestID}
				if operation == protocol.OpManagerNativeEvent {
					got = r.deliverActiveManagerEvents(ctx, as)
					want = append(want, expected[1].RequestID)
					pending, err := j.NextManagerEvent(ctx, as.RunID)
					if err != nil || len(pending) != 0 {
						t.Fatal("acknowledged event still in the journal", pending, err)
					}
				} else {
					got = r.deliverActiveManagerReceipt(ctx, ctx, as, &first)
				}
				if tc.wantTimeout {
					pending, err := j.ManagerControlRequests(ctx, as.RunID)
					if !errors.Is(got, context.DeadlineExceeded) || err != nil || len(pending) != 1 ||
						pending[0].Envelope.RequestID != first.RequestID || len(delivered) != 0 || acknowledged {
						t.Fatal("bounded legacy reply lost or acknowledged its original envelope", got, pending, delivered, err)
					}
					return
				}
				if got != nil || !slices.Equal(delivered, want) || acknowledged != (operation == protocol.OpManagerNativeReply) {
					t.Fatal("original delivery did not recover", delivered, got)
				}
			})
		})
	}
}

func TestLegacyManagerEndsWithEventUploadUnavailable(t *testing.T) {
	for _, name := range []string{"original close", "unexpected native exit"} {
		t.Run(name, func(t *testing.T) {
			const budget = 100 * time.Millisecond
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			clock := testkit.NewClock()
			j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			as := protocol.Assignment{
				Protocol: protocol.MinimumRunnerProtocol, RunID: "original-run", LeaseID: "original-lease",
				Role: protocol.RoleIssueManager, Execution: &protocol.RunExecutionPin{},
				Manager: &protocol.IssueManagerAssignment{ConversationID: "issue", NativeOwnerTools: true},
			}
			if err := j.AcceptAssignment(ctx, as, ""); err != nil {
				t.Fatal(err)
			}
			fake := testkit.NewHarness()
			fake.Turn = func(*testkit.FakeThread, string, harness.TurnInput) string { return "" }
			start, _ := json.Marshal(coordination.Request{Config: harness.ThreadConfig{Model: "model"}})
			turn, _ := json.Marshal(coordination.Request{ThreadID: "th_1", Input: harness.TurnInput{Text: "admitted work"}})
			next := 0
			var first protocol.Envelope
			var wireMu sync.Mutex
			client := &http.Client{Transport: uploadRoundTripper(func(req *http.Request) (*http.Response, error) {
				defer req.Body.Close()
				var env protocol.Envelope
				if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
					return nil, err
				}
				wireMu.Lock()
				defer wireMu.Unlock()
				var result any = map[string]any{}
				switch env.Operation {
				case protocol.OpManagerNativeClaim:
					var claim protocol.ManagerNativeClaimPayload
					if err := json.Unmarshal(env.Payload, &claim); err != nil {
						return nil, err
					}
					if !claim.AcknowledgeOnly {
						switch next {
						case 0:
							result = protocol.ManagerNativeRequest{ID: "original-start", Method: "start", Payload: start}
							next++
						case 1:
							result = protocol.ManagerNativeRequest{ID: "original-turn", Method: "turn", Payload: turn}
							next++
						case 2:
							if name == "original close" && first.RequestID != "" {
								result = protocol.ManagerNativeRequest{ID: "original-close", Method: "close", Payload: json.RawMessage(`{}`)}
								next++
							}
						}
					}
				case protocol.OpManagerNativeEvent:
					if first.RequestID == "" {
						first = env
						if name == "unexpected native exit" {
							fake.Crash()
						}
					} else if env.RequestID != first.RequestID || env.Target != first.Target || !bytes.Equal(env.Payload, first.Payload) {
						t.Error("unconfirmed journal head was replaced", first.RequestID, env.RequestID)
					}
					return managerReceiptHTTPResponse(http.StatusConflict, rpcResponse{Error: protocol.Errorf(protocol.CodeUnknownOutcome, "event receipt unavailable")})
				}
				raw, err := json.Marshal(result)
				if err != nil {
					return nil, err
				}
				return managerReceiptHTTPResponse(http.StatusOK, rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: raw}})
			})}
			r := New(Config{
				Journal: j, Executions: &eventFailureResolver{closeRecoveryResolver{native: fake}},
				Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), ServerURL: "http://runner.invalid",
				HTTPClient: client, RequestTimeout: budget, RetryDelay: 5 * time.Millisecond, Log: slog.New(slog.DiscardHandler),
			})
			done := make(chan error, 1)
			go func() { done <- r.managerNative(ctx, ctx, as) }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("the unavailable original event was reported as delivered")
				}
			case <-time.After(2 * time.Second):
				cancel()
				<-done
				t.Fatal("native shutdown waited for the unavailable event upload")
			}
			pending, err := j.NextManagerEvent(ctx, as.RunID)
			if err != nil || len(pending) == 0 || first.RequestID == "" || pending[0].Envelope.RequestID != first.RequestID {
				t.Fatal("shutdown lost the first unconfirmed event", pending, err)
			}
			closeReply, err := j.CompletedManagerClose(ctx, as.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if name == "original close" {
				if closeReply == nil || closeReply.ID != "original-close" || closeReply.LastSequence != 1 {
					t.Fatal("original close did not retain its final sequence", closeReply)
				}
			} else if closeReply != nil {
				t.Fatal("unexpected exit fabricated a close reply", closeReply)
			}
		})
	}
}

func TestLegacyManagerStartupStopsWhenRetainedEventCannotBeDelivered(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	clock := testkit.NewClock()
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{
		Protocol: protocol.MinimumRunnerProtocol, RunID: "original-run", LeaseID: "original-lease",
		Role: protocol.RoleIssueManager, Execution: &protocol.RunExecutionPin{},
		Manager: &protocol.IssueManagerAssignment{ConversationID: "issue"},
	}
	if err := j.AcceptAssignment(ctx, as, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := j.NextAttempt(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}
	if err := j.SetPhase(ctx, as.RunID, journal.PhaseStarted); err != nil {
		t.Fatal(err)
	}
	original := protocol.Envelope{RequestID: "original-event", Operation: protocol.OpManagerNativeEvent, Target: runTarget(as)}
	if err := j.SaveManagerEvent(ctx, as.RunID, &original, json.RawMessage(`{"thread_id":"original-thread"}`)); err != nil {
		t.Fatal(err)
	}
	fake := testkit.NewHarness()
	firstSend := make(chan struct{})
	var sent sync.Once
	client := &http.Client{Transport: uploadRoundTripper(func(req *http.Request) (*http.Response, error) {
		defer req.Body.Close()
		var env protocol.Envelope
		if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
			return nil, err
		}
		if env.Operation != protocol.OpManagerNativeEvent {
			t.Error("startup continued before settling the retained event", env.Operation)
			return managerReceiptHTTPResponse(http.StatusOK, rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
		}
		if req.Header.Get(headerRun) != as.RunID || env.RequestID != original.RequestID || env.Target != original.Target || !bytes.Equal(env.Payload, original.Payload) {
			t.Error("startup rewrote the retained event")
		}
		sent.Do(func() { close(firstSend) })
		return nil, errors.New("event upload unavailable")
	})}
	r := New(Config{
		Journal: j, Executions: &eventFailureResolver{closeRecoveryResolver{native: fake}},
		Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), ServerURL: "http://runner.invalid",
		HTTPClient: client, RequestTimeout: 100 * time.Millisecond, RetryDelay: 5 * time.Millisecond, Log: slog.New(slog.DiscardHandler),
	})
	done := make(chan error, 1)
	go func() { done <- r.managerNative(ctx, ctx, as) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("startup returned as if it delivered the original event")
		}
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("legacy startup exceeded its delivery budget")
	}
	select {
	case <-firstSend:
	default:
		t.Fatal("startup never attempted to deliver the retained event")
	}
	pending, err := j.NextManagerEvent(ctx, as.RunID)
	if err != nil || len(pending) != 1 || pending[0].Envelope.RequestID != original.RequestID {
		t.Fatal("startup discarded or replaced the retained journal head", pending, err)
	}
}
