package journal

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestRetainedAuthorityCompletionRequiresCurrentStopAndPhysicalProof(t *testing.T) {
	for _, scenario := range []string{"continue", "new-authority", "physical-proof-missing", "different-stop", "valid"} {
		t.Run(scenario, func(t *testing.T) {
			j, _, as := retainedStopFixture(t)
			ctx := t.Context()
			if err := j.AuthorizeRetainedStop(ctx, as.RunID, "original-stop-receipt"); err != nil {
				t.Fatal(err)
			}
			proof, err := j.RetainedStop(ctx, as.RunID)
			if err != nil || proof == nil {
				t.Fatal(proof, err)
			}
			if scenario != "physical-proof-missing" {
				if err := j.ConfirmStopped(ctx, as.RunID); err != nil {
					t.Fatal(err)
				}
			}
			env := protocol.Envelope{RequestID: "original-pending-request", Operation: protocol.OpRunsRelease,
				Target: protocol.Target{Type: "run", ID: as.RunID}, Payload: json.RawMessage(`{"reason":"unknown"}`)}
			if err := j.PutRequest(ctx, Request{RunID: as.RunID, Envelope: env}); err != nil {
				t.Fatal(err)
			}
			if err := j.AppendEvent(ctx, as.RunID, protocol.RunEvent{NativeID: "original-pending-event", Kind: "turn.observed", Body: json.RawMessage(`{"state":"unknown"}`)}); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "continue":
				err = j.ClearRetainedStop(ctx, as.RunID)
			case "new-authority":
				err = j.SetRetainedResume(ctx, as.RunID, protocol.RetainedRunResume{RequestID: "replacement-attach", Mode: "attach"})
			case "different-stop":
				err = j.AuthorizeRetainedStop(ctx, as.RunID, "different-stop-receipt")
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := j.Run(ctx, as.RunID)
			if err != nil {
				t.Fatal(err)
			}
			pending, _ := j.Requests(ctx)
			events, _ := j.PendingEvents(ctx, as.RunID, 100)
			err = j.DoneRetainedStop(ctx, as.RunID, *proof)
			if scenario != "valid" {
				if err == nil {
					t.Fatal("stale Stop or missing physical proof completed responsibility")
				}
				after, readErr := j.Run(ctx, as.RunID)
				remaining, _ := j.Requests(ctx)
				facts, _ := j.PendingEvents(ctx, as.RunID, 100)
				if readErr != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(pending, remaining) || !reflect.DeepEqual(events, facts) {
					t.Fatal("rejected atomic completion changed original facts", readErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var original string
			if err := j.db.QueryRowContext(ctx, `SELECT authority_json FROM retained_run_resumes WHERE run_id=?`, as.RunID).Scan(&original); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(original, "original-pending-request") || !strings.Contains(original, "original-pending-event") {
				t.Fatal("atomic Stop completion lost unapplied history", original)
			}
			if err := j.SetRetainedResume(ctx, as.RunID, protocol.RetainedRunResume{RequestID: "late-replacement", Mode: "attach"}); !errors.Is(err, ErrNotFound) {
				t.Fatal("late attachment rewrote completed journal authority", err)
			}
			var after string
			if err := j.db.QueryRowContext(ctx, `SELECT authority_json FROM retained_run_resumes WHERE run_id=?`, as.RunID).Scan(&after); err != nil || after != original {
				t.Fatal("completed journal history changed", after, err)
			}
		})
	}
}
