package runner

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestFault_ManagerDeliveredCloseSurvivesPeerRetirementBeforeLocalDone(t *testing.T) {
	for _, lostAck := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed_ack", true: "lost_ack"}[lostAck], func(t *testing.T) {
			clock := testkit.NewClock()
			path := filepath.Join(t.TempDir(), "journal.db")
			j, err := journal.Open(t.Context(), path, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = j.Close() }()
			as := protocol.Assignment{Protocol: 4, RunID: "original-manager-run", LeaseID: "original-lease", Role: protocol.RoleIssueManager, Execution: &protocol.RunExecutionPin{}, Manager: &protocol.IssueManagerAssignment{ConversationID: "original-issue"}}
			if err = j.AcceptAssignment(t.Context(), as, ""); err != nil {
				t.Fatal(err)
			}
			crash, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer crash.Close()
			// The process has completed its real native close and the peer ACK, but
			// its local final Run retirement cannot commit before this crash point.
			if _, err = crash.ExecContext(t.Context(), `CREATE TRIGGER crash_before_done BEFORE UPDATE OF phase ON runs WHEN NEW.phase='done' BEGIN SELECT RAISE(ABORT,'injected crash before local Done'); END`); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			retired := false
			claims, replies, acks := 0, 0, 0
			var original protocol.ManagerNativeReply
			client := &http.Client{Transport: uploadRoundTripper(func(req *http.Request) (*http.Response, error) {
				defer req.Body.Close()
				var env protocol.Envelope
				if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
					t.Fatal(err)
				}
				if req.Header.Get(headerRun) != as.RunID {
					t.Fatal("close changed Run credential")
				}
				mu.Lock()
				defer mu.Unlock()
				result := any(map[string]any{})
				switch env.Operation {
				case protocol.OpManagerNativeEvent:
				case protocol.OpManagerNativeClaim:
					var p protocol.ManagerNativeClaimPayload
					if len(env.Payload) > 0 && json.Unmarshal(env.Payload, &p) != nil {
						t.Fatal("invalid native claim", env)
					}
					if p.AcknowledgeOnly {
						if !reflect.DeepEqual(p.AcknowledgedIDs, []string{"actual-native-close"}) {
							t.Fatal("ACK changed command", p)
						}
						acks++
						// The coordinator consumed the original reply, then the peer accepted
						// the delivery ACK. It now has no command row or response to replay.
						retired = true
						if lostAck && acks == 1 {
							return nil, errors.New("peer retired command; ACK response lost")
						}
					} else {
						claims++
						if retired || claims != 1 {
							t.Fatal("recovery dispatched native work", claims)
						}
						result = protocol.ManagerNativeRequest{ID: "actual-native-close", Method: "close", Payload: json.RawMessage(`{}`)}
					}
				case protocol.OpManagerNativeReply:
					replies++
					if retired {
						return managerReceiptHTTPResponse(http.StatusConflict, rpcResponse{Error: protocol.Errorf(protocol.CodeUnknownOutcome, "original command was consumed and retired")})
					}
					if json.Unmarshal(env.Payload, &original) != nil || original.ID != "actual-native-close" || original.Error != "" {
						t.Fatal("not an actual successful native close", env)
					}
				default:
					t.Fatal("unexpected close-recovery RPC", env.Operation)
				}
				raw, _ := json.Marshal(result)
				return managerReceiptHTTPResponse(http.StatusOK, rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: raw}})
			})}
			resolver := &closeRecoveryResolver{native: testkit.NewHarness()}
			cfg := Config{Journal: j, Executions: resolver, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), ServerURL: "http://runner.invalid", HTTPClient: client, RequestTimeout: time.Second, RetryDelay: time.Millisecond, Log: slog.New(slog.DiscardHandler)}
			r := New(cfg)
			r.runnerProtocol = 4
			if err = r.managerNative(t.Context(), t.Context(), as); err == nil {
				t.Fatal("local Done crash point not reached")
			}
			closed, err := j.CompletedManagerClose(t.Context(), as.RunID)
			delivered, deliveredErr := j.ManagerCloseReplyDelivered(t.Context(), as.RunID, "actual-native-close")
			run, runErr := j.Run(t.Context(), as.RunID)
			pending, pendingErr := j.Requests(t.Context())
			ids, idsErr := j.ManagerReceiptAcknowledgements(t.Context(), as.RunID)
			if err != nil || deliveredErr != nil || runErr != nil || pendingErr != nil || idsErr != nil || closed == nil || !delivered || !run.StoppedConfirmed || len(pending) != 0 {
				t.Fatal("crash lost close proof or delivery", closed, delivered, run, pending, ids, err, deliveredErr, runErr, pendingErr, idsErr)
			}
			if len(ids) != map[bool]int{false: 0, true: 1}[lostAck] {
				t.Fatal("lost ACK was fabricated or discarded", ids)
			}
			last, err := j.LastManagerEvent(t.Context(), as.RunID)
			if err != nil || closed.LastSequence != last || last != original.LastSequence {
				t.Fatal("actual close cursor changed", closed, last, original, err)
			}
			if _, err = crash.ExecContext(t.Context(), `DROP TRIGGER crash_before_done`); err != nil {
				t.Fatal(err)
			}
			if err = crash.Close(); err != nil {
				t.Fatal(err)
			}
			if err = j.Close(); err != nil {
				t.Fatal(err)
			}
			j, err = journal.Open(t.Context(), path, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Journal = j
			r = New(cfg)
			r.runnerProtocol = 4
			r.replayManagerAcknowledgements(t.Context())
			// This is the real CompletedManagerClose -> replayManagerClose entrance,
			// after the peer has genuinely forgotten the consumed close command.
			if err = r.managerNative(t.Context(), t.Context(), as); err != nil {
				t.Fatal("delivered close could not finish without recreating its reply", err)
			}
			if _, err = j.Run(t.Context(), as.RunID); !errors.Is(err, journal.ErrNotFound) {
				t.Fatal("closed Run retained a local execution slot", err)
			}
			if got, err := j.LastManagerEvent(t.Context(), as.RunID); err != nil || got != last {
				t.Fatal("recovery emitted a fact after physical close", got, last, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if !retired || claims != 1 || replies != 1 || acks != 1+map[bool]int{false: 0, true: 1}[lostAck] || resolver.opens.Load() != 1 {
				t.Fatal("recovery recreated an already delivered close", retired, claims, replies, acks, resolver.opens.Load())
			}
		})
	}
}
