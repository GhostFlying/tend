package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestLegacyManagerDeliversOriginalFactsAfterRequestBudget(t *testing.T) {
	const budget = 100 * time.Millisecond
	for _, operation := range []string{protocol.OpManagerNativeEvent, protocol.OpManagerNativeReply} {
		t.Run(operation, func(t *testing.T) {
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
					if time.Since(started) <= budget {
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
				if got != nil || !slices.Equal(delivered, want) || acknowledged != (operation == protocol.OpManagerNativeReply) {
					t.Fatal("original delivery did not recover after one request budget", delivered, got)
				}
			})
		})
	}
}
