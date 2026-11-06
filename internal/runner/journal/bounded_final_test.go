package journal

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func boundedFinalJournalFixture(t *testing.T) (*Journal, protocol.Assignment, Request, protocol.BoundedMaintenanceAuthorization) {
	t.Helper()
	j, _, as := retainedStopFixture(t)
	if err := j.SetPhase(t.Context(), as.RunID, PhaseSubmitting); err != nil {
		t.Fatal(err)
	}
	if err := j.ConfirmStopped(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	request := Request{RunID: as.RunID, Envelope: protocol.Envelope{RequestID: "original-final", Operation: protocol.OpRunsSubmitArtifact,
		Target: protocol.Target{Type: "run", ID: as.RunID}, Payload: json.RawMessage(`{"original":"result"}`)}}
	if err := j.PutRequest(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := j.NextProcessGeneration(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, err := j.BoundedDataIdentity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	authority := protocol.BoundedMaintenanceAuthorization{RequestID: "original-force", Role: "runner", DataIdentity: &data}
	if err := j.QuarantineBoundedRecovery(t.Context(), authority); err != nil {
		t.Fatal(err)
	}
	return j, as, request, authority
}

func TestBoundedFinalAcknowledgementRequiresFrozenOriginalFact(t *testing.T) {
	for _, scenario := range []string{"payload", "target", "request-id", "accepted", "result-request-id", "missing-stop", "assignment", "late-request", "unrelated-role"} {
		t.Run(scenario, func(t *testing.T) {
			j, as, request, _ := boundedFinalJournalFixture(t)
			result := protocol.Operation{RequestID: request.Envelope.RequestID, Status: protocol.OpSucceeded}
			switch scenario {
			case "payload":
				request.Envelope.Payload = json.RawMessage(`{"different":"result"}`)
			case "target":
				request.Envelope.Target.ID = "different-run"
			case "request-id":
				request.Envelope.RequestID, result.RequestID = "different-request", "different-request"
			case "accepted":
				result.Status = protocol.OpAccepted
			case "result-request-id":
				result.RequestID = "different-request"
			case "missing-stop":
				if _, err := j.NextAttempt(t.Context(), as.RunID); err != nil {
					t.Fatal(err)
				}
			case "assignment":
				as.LeaseID = "different-lease"
				body, _ := json.Marshal(as)
				if _, err := j.db.ExecContext(t.Context(), `UPDATE runs SET assignment_json=? WHERE run_id=?`, string(body), as.RunID); err != nil {
					t.Fatal(err)
				}
			case "late-request":
				request.Envelope.RequestID, result.RequestID = "late-unfrozen-request", "late-unfrozen-request"
				if err := j.PutRequest(t.Context(), request); err != nil {
					t.Fatal(err)
				}
			case "unrelated-role":
				request.Envelope.Operation = protocol.OpRunsRelease
			}
			before, _ := j.Run(t.Context(), as.RunID)
			pending, _ := j.Requests(t.Context())
			if err := j.AcknowledgeBoundedFinal(t.Context(), request, result); err == nil {
				t.Fatal("incomplete or mismatched original fact was acknowledged")
			}
			after, _ := j.Run(t.Context(), as.RunID)
			remaining, _ := j.Requests(t.Context())
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(pending, remaining) {
				t.Fatal("rejected acknowledgement changed original run or requests", after, remaining)
			}
		})
	}
}

func TestBoundedFinalStopPreservesUnappliedFactsAndPriorAuthority(t *testing.T) {
	j, as, request, authority := boundedFinalJournalFixture(t)
	ctx := t.Context()
	if err := j.AcknowledgeBoundedFinal(ctx, request, protocol.Operation{RequestID: request.Envelope.RequestID, Status: protocol.OpSucceeded}); err != nil {
		t.Fatal(err)
	}
	// Replacing the runner again cannot invalidate an already committed fact,
	// and cannot make it permission to start the old native work.
	authority.RequestID = "second-force"
	if err := j.QuarantineBoundedRecovery(ctx, authority); err != nil {
		t.Fatal(err)
	}
	permit, err := j.RetainedResume(ctx, as.RunID)
	if err != nil || permit == nil || permit.RequestID != "attach-authority" {
		t.Fatal("acknowledgement erased or replaced original strict authority", permit, err)
	}
	protected, err := j.BoundedRunQuarantined(ctx, as.RunID)
	if err != nil || !protected {
		t.Fatal("acknowledgement authorized a bounded attachment", protected, err)
	}
	unknown := Request{RunID: as.RunID, Envelope: protocol.Envelope{RequestID: "original-unknown-receipt", Operation: protocol.OpWorkspaceCleanup,
		Target: protocol.Target{Type: "run", ID: as.RunID}, Payload: json.RawMessage(`{"state":"unknown"}`)}}
	if err := j.PutRequest(ctx, unknown); err != nil {
		t.Fatal(err)
	}
	if err := j.AppendEvent(ctx, as.RunID, protocol.RunEvent{NativeID: "original-event", Kind: "turn.observed", Body: json.RawMessage(`{"state":"unknown"}`)}); err != nil {
		t.Fatal(err)
	}
	stop, err := j.AuthorizeBoundedFinalStop(ctx, as, "fresh-host-hello")
	if err != nil || stop == nil {
		t.Fatal("original terminal evidence did not authorize matching host Stop", stop, err)
	}
	if err := j.DoneRetainedStop(ctx, as.RunID, *stop); err != nil {
		t.Fatal(err)
	}
	var metadata string
	if err := j.db.QueryRowContext(ctx, `SELECT authority_json FROM retained_run_resumes WHERE run_id=?`, as.RunID).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	for _, original := range []string{"original-final", "original-force", "fresh-host-hello", "original-unknown-receipt", "original-event", `\"state\":\"unknown\"`} {
		if !strings.Contains(metadata, original) {
			t.Fatalf("terminal settlement lost original fact %q: %s", original, metadata)
		}
	}
}

func TestBoundedFinalStopRejectsChangedOriginalBindingOrPhysicalProof(t *testing.T) {
	for _, scenario := range []string{"lease", "spec", "missing-stop", "different-worker", "different-attempt", "blank-hello"} {
		t.Run(scenario, func(t *testing.T) {
			j, as, request, _ := boundedFinalJournalFixture(t)
			if err := j.AcknowledgeBoundedFinal(t.Context(), request, protocol.Operation{RequestID: request.Envelope.RequestID, Status: protocol.OpSucceeded}); err != nil {
				t.Fatal(err)
			}
			hello := "fresh-host-hello"
			switch scenario {
			case "lease":
				as.LeaseID = "different-lease"
			case "spec":
				as.SpecVersion++
			case "missing-stop":
				if _, err := j.NextAttempt(t.Context(), as.RunID); err != nil {
					t.Fatal(err)
				}
			case "different-worker":
				if err := j.SetThread(t.Context(), as.RunID, "different-native-thread"); err != nil {
					t.Fatal(err)
				}
			case "different-attempt":
				if _, err := j.NextAttempt(t.Context(), as.RunID); err != nil {
					t.Fatal(err)
				}
				if err := j.ConfirmStopped(t.Context(), as.RunID); err != nil {
					t.Fatal(err)
				}
			case "blank-hello":
				hello = ""
			}
			stop, err := j.AuthorizeBoundedFinalStop(t.Context(), as, hello)
			if stop != nil || scenario == "blank-hello" && err == nil || scenario != "blank-hello" && err != nil {
				t.Fatal("changed original binding authorized settlement", stop, err)
			}
			if _, err := j.Run(t.Context(), request.RunID); err != nil {
				t.Fatal("rejected Stop released original run", err)
			}
		})
	}
}

func TestBoundedManagerTerminalAcknowledgementRequiresOriginalClosureFacts(t *testing.T) {
	for _, scenario := range []string{"close", "stopped", "stopped-without-close", "accepted", "missing-physical-proof", "wrong-close-id", "wrong-close-result", "wrong-close-cursor", "wrong-stop-cursor", "ordinary-turn-reply"} {
		t.Run(scenario, func(t *testing.T) {
			j, _, original := retainedStopFixture(t)
			as := original
			as.RunID, as.LeaseID, as.Role = "original-manager", "original-manager-lease", protocol.RoleIssueManager
			if err := j.AcceptAssignment(t.Context(), as, ""); err != nil {
				t.Fatal(err)
			}
			if err := j.SetPhase(t.Context(), as.RunID, PhaseReleasing); err != nil {
				t.Fatal(err)
			}
			if scenario != "missing-physical-proof" {
				if err := j.ConfirmStopped(t.Context(), as.RunID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := j.db.ExecContext(t.Context(), `INSERT INTO manager_event_sequences(run_id,next_seq) VALUES(?,87)`, as.RunID); err != nil {
				t.Fatal(err)
			}
			command := protocol.ManagerNativeRequest{ID: "original-close", Method: "close", Payload: json.RawMessage(`{}`)}
			if scenario == "ordinary-turn-reply" {
				command.Method = "turn"
			}
			reply := protocol.ManagerNativeReply{ID: command.ID, Result: json.RawMessage(`null`), LastSequence: 86}
			if scenario != "stopped-without-close" {
				if _, _, err := j.ManagerOperation(t.Context(), as.RunID, command); err != nil {
					t.Fatal(err)
				}
				if err := j.CompleteManagerOperation(t.Context(), reply); err != nil {
					t.Fatal(err)
				}
			}
			envelope := protocol.Envelope{RequestID: "original-close-envelope", Operation: protocol.OpManagerNativeReply, Target: protocol.Target{Type: "run", ID: as.RunID}}
			switch scenario {
			case "wrong-close-id":
				reply.ID = "different-close"
			case "wrong-close-result":
				reply.Result = json.RawMessage(`"different-result"`)
			case "wrong-close-cursor":
				reply.LastSequence--
			}
			envelope.Payload, _ = json.Marshal(reply)
			if scenario == "stopped" || scenario == "stopped-without-close" || scenario == "wrong-stop-cursor" {
				envelope.Operation = protocol.OpManagerNativeStopped
				stopped := protocol.ManagerStoppedPayload{Reason: "original worker stopped", LastSequence: 86}
				if scenario == "wrong-stop-cursor" {
					stopped.LastSequence--
				}
				envelope.Payload, _ = json.Marshal(stopped)
			}
			request := Request{RunID: as.RunID, Envelope: envelope}
			if err := j.PutRequest(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			if _, err := j.NextProcessGeneration(t.Context()); err != nil {
				t.Fatal(err)
			}
			data, err := j.BoundedDataIdentity(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err := j.QuarantineBoundedRecovery(t.Context(), protocol.BoundedMaintenanceAuthorization{RequestID: "original-force", Role: "runner", DataIdentity: &data}); err != nil {
				t.Fatal(err)
			}
			result := protocol.Operation{RequestID: request.Envelope.RequestID, Status: protocol.OpSucceeded}
			if scenario == "accepted" {
				result.Status = protocol.OpAccepted
			}
			err = j.AcknowledgeBoundedFinal(t.Context(), request, result)
			valid := scenario == "close" || scenario == "stopped" || scenario == "stopped-without-close"
			if (err == nil) != valid {
				t.Fatal("manager terminal acknowledgement crossed closure contract", err)
			}
			stop, err := j.AuthorizeBoundedFinalStop(t.Context(), as, "fresh-host-stop")
			if err != nil || (stop != nil) != valid {
				t.Fatal("manager Stop did not preserve original closure boundary", stop, err)
			}
			if permit, err := j.RetainedResume(t.Context(), as.RunID); err != nil || permit != nil {
				t.Fatal("manager terminal fact manufactured attachment authority", permit, err)
			}
		})
	}
}
