package runner

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestFault_ManagerExitObservedDuringReplyOutage(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "event_sync"}[async], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			clock := testkit.NewClock()
			j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			as := protocol.Assignment{EventSync: async, RunID: "original-run", LeaseID: "original-lease", Role: protocol.RoleIssueManager, Execution: &protocol.RunExecutionPin{}, Manager: &protocol.IssueManagerAssignment{ConversationID: "issue"}}
			if err := j.AcceptAssignment(ctx, as, ""); err != nil {
				t.Fatal(err)
			}
			fake := testkit.NewHarness()
			var crashed sync.Once
			observed := make(chan struct{}, 1)
			var replies, stops atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
					t.Error(err)
					return
				}
				var result any = map[string]any{}
				switch env.Operation {
				case protocol.OpManagerNativeClaim:
					result = protocol.ManagerNativeRequest{ID: "original-models", Method: "models", Payload: json.RawMessage(`{}`)}
				case protocol.OpManagerNativeReply:
					replies.Add(1)
					crashed.Do(fake.Crash)
					w.WriteHeader(http.StatusConflict)
					_ = json.NewEncoder(w).Encode(rpcResponse{Error: protocol.Errorf(protocol.CodeUnknownOutcome, "reply endpoint unavailable")})
					return
				case protocol.OpManagerNativeStopped:
					stops.Add(1)
					select {
					case observed <- struct{}{}:
					default:
					}
				}
				raw, _ := json.Marshal(result)
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: raw}})
			}))
			defer server.Close()
			r := New(Config{Journal: j, Executions: &closeRecoveryResolver{native: fake}, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), ServerURL: server.URL, HTTPClient: server.Client(), RequestTimeout: 40 * time.Millisecond, RetryDelay: 5 * time.Millisecond, Log: slog.New(slog.DiscardHandler)})
			done := make(chan struct{})
			go func() {
				defer close(done)
				_ = r.executeManager(ctx, ctx, &active{as: as, desired: protocol.DesiredRun})
			}()
			defer func() { cancel(); <-done }()
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			select {
			case <-observed:
			case <-done:
			case <-timer.C:
				t.Fatal("native exit was never observed")
			}
			saved, err := j.HistoricalRun(t.Context(), as.RunID)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("after native exit: replies=%d physical-stop-RPCs=%d stopped-confirmed=%v", replies.Load(), stops.Load(), saved.StoppedConfirmed)
			if async && stops.Load() == 0 {
				t.Error("native exit is not reported while unrelated reply endpoint is unavailable")
			}
		})
	}
}
