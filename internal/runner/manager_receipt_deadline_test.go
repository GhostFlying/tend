package runner

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestFault_NormalManagerReceiptDeadlineReleasesOnlyActualClosedProcess(t *testing.T) {
	for _, delayed := range []bool{true, false} {
		name := "deadline"
		if !delayed {
			name = "transient-within-budget"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cleanup := context.WithCancel(t.Context())
			defer cleanup()
			clock := testkit.NewClock()
			j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			as := protocol.Assignment{Protocol: protocol.CurrentRunnerProtocol, RunID: "normal-manager", LeaseID: "original-lease", Role: protocol.RoleIssueManager, Desired: protocol.DesiredRun, Execution: &protocol.RunExecutionPin{}, Manager: &protocol.IssueManagerAssignment{ConversationID: "original-issue"}}
			if err := j.AcceptAssignment(ctx, as, ""); err != nil {
				t.Fatal(err)
			}
			event := protocol.Envelope{RequestID: "original-event", Operation: protocol.OpManagerNativeEvent, Target: runTarget(as)}
			if err := j.SaveManagerEvent(ctx, as.RunID, &event, json.RawMessage(`{"disconnected":true}`)); err != nil {
				t.Fatal(err)
			}
			if err := j.DeleteRequest(ctx, event.RequestID); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var original *protocol.Envelope
			var originalHash string
			attempts, claims := 0, 0
			stopped := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				var result any = map[string]any{}
				switch env.Operation {
				case protocol.OpManagerNativeClaim:
					claims++
					result = protocol.ManagerNativeRequest{ID: "original-native-close", Method: "close", Payload: json.RawMessage(`{}`)}
				case protocol.OpManagerNativeReply:
					attempts++
					hash, err := protocol.PayloadHash(&env)
					if err != nil {
						t.Error(err)
					}
					if original == nil {
						saved := env
						original, originalHash = &saved, hash
					} else if !sameManagerReceiptEnvelope(*original, env) || hash != originalHash {
						t.Error("receipt retry changed original identity or payload")
					}
					var reply protocol.ManagerNativeReply
					if err := json.Unmarshal(env.Payload, &reply); err != nil || reply.ID != "original-native-close" || reply.LastSequence != 1 || reply.Error != "" {
						t.Error("normal close changed its original result", reply, err)
					}
					if delayed && !stopped || !delayed && attempts <= 2 {
						w.WriteHeader(http.StatusConflict)
						_ = json.NewEncoder(w).Encode(rpcResponse{Error: protocol.Errorf(protocol.CodeUnknownOutcome, "native close must cover all applied events")})
						return
					}
				case protocol.OpManagerNativeStopped:
					if ctx.Err() != nil {
						t.Error("external cancellation was required to deliver physical stop")
					}
					var p protocol.ManagerStoppedPayload
					if err := json.Unmarshal(env.Payload, &p); err != nil || p.LastSequence != 1 {
						t.Error("stop changed the frozen close cursor", p, err)
					}
					stopped = true
				default:
					t.Error("normal close recovery invoked new work", env.Operation)
				}
				raw, _ := json.Marshal(result)
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: raw}})
			}))
			defer server.Close()
			resolver := &closeRecoveryResolver{native: testkit.NewHarness()}
			r := New(Config{Journal: j, Executions: resolver, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), ServerURL: server.URL, HTTPClient: server.Client(), RequestTimeout: time.Second, RetryDelay: time.Millisecond, Log: slog.New(slog.DiscardHandler)})
			var runErr error
			done := make(chan struct{})
			go func() {
				runErr = r.executeManager(ctx, ctx, &active{as: as, desired: protocol.DesiredRun})
				close(done)
			}()
			stop := sync.OnceFunc(func() { cleanup(); <-done })
			defer stop()
			select {
			case <-done:
				if runErr != nil {
					t.Fatal(runErr)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("normal receipt retry never yielded its completed native close to physical-stop reporting")
			}
			if ctx.Err() != nil {
				t.Fatal("normal recovery depended on caller cancellation")
			}
			mu.Lock()
			defer mu.Unlock()
			if claims != 1 || resolver.opens.Load() != 1 || attempts < 3 || stopped != delayed {
				t.Fatal("receipt deadline replayed native work or changed transient behavior", claims, resolver.opens.Load(), attempts, stopped)
			}
			if _, err := j.Run(ctx, as.RunID); !errors.Is(err, journal.ErrNotFound) {
				t.Fatal("actual closed process did not settle original journal", err)
			}
			closed, err := j.CompletedManagerClose(ctx, as.RunID)
			if err != nil || closed == nil || closed.LastSequence != 1 {
				t.Fatal("normal recovery lost original completed close", closed, err)
			}
		})
	}
}
