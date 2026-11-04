package journal

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func retainedStopFixture(t *testing.T) (*Journal, string, protocol.Assignment) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := Open(t.Context(), path, func() time.Time { return time.UnixMilli(1000) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	as := protocol.Assignment{Protocol: protocol.CurrentRunnerProtocol, RunID: "original", LeaseID: "original-lease", TaskID: "task", Generation: 3, SpecVersion: 2, Role: protocol.RoleImplement}
	if err = j.AcceptAssignment(t.Context(), as, ""); err != nil {
		t.Fatal(err)
	}
	if err = j.SetThread(t.Context(), as.RunID, "original-native-unknown"); err != nil {
		t.Fatal(err)
	}
	if err = j.SetRetainedResume(t.Context(), as.RunID, protocol.RetainedRunResume{RequestID: "attach-authority", Mode: "attach"}); err != nil {
		t.Fatal(err)
	}
	return j, path, as
}

func TestRetainedStopPersistsAndArchivesUnappliedFacts(t *testing.T) {
	j, path, as := retainedStopFixture(t)
	ctx := t.Context()
	before, err := j.Run(ctx, as.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.AuthorizeRetainedStop(ctx, as.RunID, "host-hello-request"); err != nil {
		t.Fatal(err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open(ctx, path, func() time.Time { return time.UnixMilli(1000) })
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	proof, err := j.RetainedStop(ctx, as.RunID)
	if err != nil || proof == nil || proof.HelloRequestID != "host-hello-request" {
		t.Fatal("Stop identity lost after restart", proof, err)
	}
	after, err := j.Run(ctx, as.RunID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("Stop rewrote original identity", after, err)
	}
	env := protocol.Envelope{RequestID: "original-business-request", Operation: protocol.OpRunsRelease, Target: protocol.Target{Type: "run", ID: as.RunID}, Payload: json.RawMessage(`{"reason":"unknown"}`)}
	if err = j.PutRequest(ctx, Request{RunID: as.RunID, Envelope: env}); err != nil {
		t.Fatal(err)
	}
	if err = j.AppendEvent(ctx, as.RunID, protocol.RunEvent{NativeID: "original-root", Kind: "turn.observed", Body: json.RawMessage(`{"state":"unknown"}`)}); err != nil {
		t.Fatal(err)
	}
	if err = j.Done(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}
	var history string
	if err = j.db.QueryRowContext(ctx, `SELECT json_extract(authority_json,'$.stop_history') FROM retained_run_resumes WHERE run_id=?`, as.RunID).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(history, "original-business-request") || !strings.Contains(history, "original-root") || !strings.Contains(history, `\"state\":\"unknown\"`) {
		t.Fatal("pending facts were not preserved as unapplied history", history)
	}
	if err = j.Done(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}
	var replay string
	if err = j.db.QueryRowContext(ctx, `SELECT json_extract(authority_json,'$.stop_history') FROM retained_run_resumes WHERE run_id=?`, as.RunID).Scan(&replay); err != nil || replay != history {
		t.Fatal("idempotent cleanup rewrote original history", replay, err)
	}
	pending, err := j.Requests(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatal("independently stopped responsibility stayed scheduled", pending, err)
	}
	saved, err := j.HistoricalRun(ctx, as.RunID)
	if err != nil || saved.Thread != before.Thread || !reflect.DeepEqual(saved.Assignment, before.Assignment) {
		t.Fatal("cleanup rewrote original native unknown/identity", saved, err)
	}
}

func TestRetainedStopInvalidationKeepsAttachAuthority(t *testing.T) {
	for _, kind := range []string{"retain", "new-authority", "changed-assignment"} {
		t.Run(kind, func(t *testing.T) {
			j, _, as := retainedStopFixture(t)
			ctx := t.Context()
			if err := j.AuthorizeRetainedStop(ctx, as.RunID, "host-hello-request"); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "retain":
				if err := j.ClearRetainedStop(ctx, as.RunID); err != nil {
					t.Fatal(err)
				}
			case "new-authority":
				if err := j.SetRetainedResume(ctx, as.RunID, protocol.RetainedRunResume{RequestID: "next-attach", Mode: "attach"}); err != nil {
					t.Fatal(err)
				}
			case "changed-assignment":
				as.LeaseID = "different-lease"
				raw, _ := json.Marshal(as)
				if _, err := j.db.ExecContext(ctx, `UPDATE runs SET assignment_json=? WHERE run_id=?`, string(raw), as.RunID); err != nil {
					t.Fatal(err)
				}
			}
			proof, err := j.RetainedStop(ctx, as.RunID)
			if err != nil || proof != nil {
				t.Fatal("stale Stop still authorizes settlement", proof, err)
			}
			authority, err := j.RetainedResume(ctx, as.RunID)
			if err != nil || authority == nil || authority.Mode != "attach" {
				t.Fatal("invalidating Stop erased strict boundary", authority, err)
			}
		})
	}
}
