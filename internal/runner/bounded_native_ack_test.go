package runner

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func TestBoundedNativeReplyRetainsOnlyActualDeliveryAcknowledgement(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "lost_ack_restart"}[confirmed], func(t *testing.T) {
			r, as, originalClose, _ := stoppedManagerRecoveryJournal(t, true)
			authority := protocol.BoundedMaintenanceAuthorization{RequestID: "original-force", Role: "runner"}
			bindRunnerBoundedTestAuthority(t, r.cfg.Journal, &authority)
			if err := r.cfg.Journal.QuarantineBoundedRecovery(t.Context(), authority); err != nil {
				t.Fatal(err)
			}
			if err := r.loadBoundedRecovery(t.Context()); err != nil {
				t.Fatal(err)
			}
			acknowledgements := 0
			r.c.base = "http://runner.invalid"
			r.c.http = &http.Client{Transport: uploadRoundTripper(func(request *http.Request) (*http.Response, error) {
				defer request.Body.Close()
				var env protocol.Envelope
				if err := json.NewDecoder(request.Body).Decode(&env); err != nil {
					t.Fatal(err)
				}
				if request.Header.Get(headerRun) != as.RunID {
					t.Fatal("bounded fact changed original run", env)
				}
				status := protocol.OpSucceeded
				switch env.Operation {
				case protocol.OpManagerNativeStopped:
				case protocol.OpManagerNativeReply:
					if !sameManagerReceiptEnvelope(env, originalClose) {
						t.Fatal("bounded reply changed original command", env)
					}
					if !confirmed {
						status = protocol.OpUnknown
					}
				case protocol.OpManagerNativeClaim:
					var input protocol.ManagerNativeClaimPayload
					if json.Unmarshal(env.Payload, &input) != nil || !input.AcknowledgeOnly || !reflect.DeepEqual(input.AcknowledgedIDs, []string{"original-close"}) {
						t.Fatal("delivery acknowledgement could dispatch work", env)
					}
					acknowledgements++
					if acknowledgements == 1 {
						return nil, errors.New("peer applied compact ACK; reply lost")
					}
				default:
					t.Fatal("bounded fact replay invoked new business", env.Operation)
				}
				return managerReceiptHTTPResponse(http.StatusOK, rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: status, Result: json.RawMessage(`{}`)}})
			})}
			// Replay runs before Hello. Its original protocol4 assignment is
			// enough to retain the delivery fact, never to send a fresh claim.
			r.replayBoundedFacts(t.Context())
			ids, err := r.cfg.Journal.ManagerReceiptAcknowledgements(t.Context(), as.RunID)
			if err != nil || acknowledgements != 0 {
				t.Fatal(ids, acknowledgements, err)
			}
			delivered, deliveryErr := r.cfg.Journal.ManagerCloseReplyDelivered(t.Context(), as.RunID, "original-close")
			if deliveryErr != nil || delivered != confirmed {
				t.Fatal("bounded reply delivery differs from actual peer result", delivered, confirmed, deliveryErr)
			}
			pending, err := r.cfg.Journal.Requests(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if !confirmed {
				if len(ids) != 0 || len(pending) != 1 || !sameManagerReceiptEnvelope(pending[0].Envelope, originalClose) {
					t.Fatal("unknown close became an ACK or lost its original envelope", ids, pending)
				}
				run, err := r.cfg.Journal.Run(t.Context(), as.RunID)
				if err != nil || !run.StoppedConfirmed || run.Phase != journal.PhaseReleasing {
					t.Fatal("unknown business reply changed physical stop", run, err)
				}
				return
			}
			if !reflect.DeepEqual(ids, []string{"original-close"}) || len(pending) != 0 {
				t.Fatal("confirmed reply retired before durable compact ACK", ids, pending)
			}
			r.runnerProtocol = 4
			if err := r.flushManagerAcknowledgements(t.Context(), as); err == nil {
				t.Fatal("lost compact ACK was treated as received")
			}
			if err := r.cfg.Journal.Done(t.Context(), as.RunID); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(r.cfg.Journal.Directory(), "journal.db")
			if err := r.cfg.Journal.Close(); err != nil {
				t.Fatal(err)
			}
			j, err := journal.Open(t.Context(), path, r.cfg.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			cfg := r.cfg
			cfg.Journal = j
			restarted := New(cfg)
			restarted.c.base, restarted.c.http, restarted.runnerProtocol = r.c.base, r.c.http, 4
			restarted.replayManagerAcknowledgements(t.Context())
			if ids, err := j.ManagerReceiptAcknowledgements(t.Context(), as.RunID); err != nil || len(ids) != 0 || acknowledgements != 2 {
				t.Fatal("restart lost compact ACK or replayed business", ids, acknowledgements, err)
			}
			if _, err := j.Run(t.Context(), as.RunID); !errors.Is(err, journal.ErrNotFound) {
				t.Fatal("delivery metadata retained an execution slot", err)
			}
		})
	}
}
