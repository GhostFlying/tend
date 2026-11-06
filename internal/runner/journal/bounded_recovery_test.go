package journal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestBoundedRecoveryQuarantinePreservesOriginalUnknownWorkAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j := open(t, path)
	ctx := t.Context()
	as := protocol.Assignment{Protocol: protocol.CurrentRunnerProtocol, RunID: "original-run", LeaseID: "original-lease", Role: protocol.RoleImplement, TaskID: "original-task", Generation: 3, SpecVersion: 2}
	if err := j.AcceptAssignment(ctx, as, ""); err != nil {
		t.Fatal(err)
	}
	if err := j.SetPhase(ctx, as.RunID, PhaseStarted); err != nil {
		t.Fatal(err)
	}
	if err := j.SetHeld(ctx, as.RunID, true); err != nil {
		t.Fatal(err)
	}
	if err := j.SetThread(ctx, as.RunID, "original-native-thread"); err != nil {
		t.Fatal(err)
	}
	request := Request{RunID: as.RunID, Envelope: protocol.Envelope{RequestID: "original-unknown-request", Operation: protocol.OpRunsRelease, Payload: json.RawMessage(`{"reason":"unknown"}`)}}
	if err := j.PutRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	before, err := j.Run(ctx, as.RunID)
	if err != nil {
		t.Fatal(err)
	}
	requests, err := j.Requests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	authority := protocol.BoundedMaintenanceAuthorization{RequestID: "independent-force-authority", Role: "runner", Reason: "replace stalled process", ExpiresAtMillis: time.Now().Add(time.Minute).UnixMilli()}
	bindBoundedTestAuthority(t, j, &authority)
	if err := j.QuarantineBoundedRecovery(ctx, authority); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j = open(t, path)
	defer j.Close()
	after, err := j.Run(ctx, as.RunID)
	before.Fenced = true
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("replacement changed original execution facts: before=%+v after=%+v err=%v", before, after, err)
	}
	afterRequests, err := j.Requests(ctx)
	if err != nil || !reflect.DeepEqual(afterRequests, requests) {
		t.Fatalf("replacement rewrote unknown request identities: %+v %v", afterRequests, err)
	}
	saved, err := j.BoundedRecovery(ctx)
	if err != nil || saved == nil || !reflect.DeepEqual(saved.Authorization, authority) {
		t.Fatalf("durable quarantine was lost: %+v %v", saved, err)
	}
	originalSnapshot := string(saved.Snapshot)
	if err := j.QuarantineBoundedRecovery(ctx, authority); err != nil {
		t.Fatal(err)
	}
	saved, err = j.BoundedRecovery(ctx)
	if err != nil || string(saved.Snapshot) != originalSnapshot {
		t.Fatalf("same authorization replaced the original capture: %+v %v", saved, err)
	}
	extended := authority
	extended.ExpiresAtMillis++
	if err := j.QuarantineBoundedRecovery(ctx, extended); err == nil {
		t.Fatal("same authority identity extended its deadline")
	}
	var snapshot struct {
		Runs []struct {
			Assignment protocol.Assignment `json:"assignment"`
			Fenced     int                 `json:"fenced"`
			Held       int                 `json:"held"`
		} `json:"runs"`
		Requests []struct {
			RequestID string `json:"request_id"`
		} `json:"requests"`
	}
	// SQLite stores integer booleans in the immutable diagnostic capture.
	if err := json.Unmarshal(saved.Snapshot, &snapshot); err != nil || len(snapshot.Runs) != 1 || len(snapshot.Requests) != 1 ||
		!reflect.DeepEqual(snapshot.Runs[0].Assignment, before.Assignment) || snapshot.Runs[0].Fenced != 0 || snapshot.Runs[0].Held != 1 || snapshot.Requests[0].RequestID != request.Envelope.RequestID {
		t.Fatalf("missing original run/request capture: %+v %v", snapshot, err)
	}
}

func TestBoundedRecoveryContinuationRequiresIndependentAuthorityAndFrozenRun(t *testing.T) {
	j := open(t, filepath.Join(t.TempDir(), "journal.db"))
	defer j.Close()
	ctx := t.Context()
	as := protocol.Assignment{RunID: "original-run", LeaseID: "original-lease", Role: protocol.RoleImplement}
	if err := j.AcceptAssignment(ctx, as, ""); err != nil {
		t.Fatal(err)
	}
	authority := protocol.BoundedMaintenanceAuthorization{RequestID: "forced-replacement", Role: "runner"}
	bindBoundedTestAuthority(t, j, &authority)
	if err := j.QuarantineBoundedRecovery(ctx, authority); err != nil {
		t.Fatal(err)
	}
	permit := protocol.RetainedRunResume{RequestID: "independent-owner-continuation", Mode: "attach"}
	if err := j.SetRetainedResume(ctx, as.RunID, permit); err == nil {
		t.Fatal("ordinary attachment authority cleared forced isolation")
	}
	permit.RecoveryAuthorizationID = authority.RequestID
	if err := j.SetRetainedResume(ctx, as.RunID, permit); err != nil {
		t.Fatalf("independent original-run continuation was rejected: %v", err)
	}
	late := protocol.Assignment{RunID: "unfrozen-run", LeaseID: "another-lease", Role: protocol.RoleImplement}
	if err := j.AcceptAssignment(ctx, late, ""); err != nil {
		t.Fatal(err)
	}
	if err := j.SetRetainedResume(ctx, late.RunID, permit); err == nil {
		t.Fatal("continuation broadened the original frozen run set")
	}
	if err := j.exec(ctx, `UPDATE runs SET assignment_json=json_set(assignment_json,'$.lease_id','changed-lease') WHERE run_id=?`, as.RunID); err != nil {
		t.Fatal(err)
	}
	if err := j.SetRetainedResume(ctx, as.RunID, permit); err == nil {
		t.Fatal("continuation accepted a changed original assignment")
	}
}

func bindBoundedTestAuthority(t *testing.T, j *Journal, authority *protocol.BoundedMaintenanceAuthorization) {
	t.Helper()
	if _, err := j.NextProcessGeneration(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, err := j.BoundedDataIdentity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	authority.DataIdentity = &data
}

func TestBoundedRecoveryValidatesIncarnationBeforeMigratingOriginalJournal15(t *testing.T) {
	for _, wrong := range []bool{true, false} {
		t.Run(fmt.Sprintf("wrong_incarnation_%t", wrong), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "journal.db")
			writer, err := acquireWriter(path)
			if err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := fs.ReadDir(migrationFS, "migrations")
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries[:15] {
				body, err := migrationFS.ReadFile("migrations/" + entry.Name())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(t.Context(), string(body)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.ExecContext(t.Context(), `PRAGMA user_version=15`); err != nil {
				t.Fatal(err)
			}
			old := &Journal{path: path, db: db, writer: writer, now: time.Now}
			as := protocol.Assignment{RunID: "original-journal15-run", LeaseID: "original-lease", Role: protocol.RoleImplement}
			if err := old.AcceptAssignment(t.Context(), as, ""); err != nil {
				t.Fatal(err)
			}
			if err := old.SetHeld(t.Context(), as.RunID, true); err != nil {
				t.Fatal(err)
			}
			request := Request{RunID: as.RunID, Envelope: protocol.Envelope{RequestID: "original-unknown-request", Operation: protocol.OpRunsRelease}}
			if err := old.PutRequest(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			authority := protocol.BoundedMaintenanceAuthorization{RequestID: "independent-force", Role: "runner"}
			bindBoundedTestAuthority(t, old, &authority)
			if authority.DataIdentity.Version != 15 {
				t.Fatal("fixture is not the committed journal15 format")
			}
			if err := old.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if wrong {
				authority.DataIdentity.Incarnation += "-different"
			}
			upgraded, err := OpenForBoundedRecovery(t.Context(), path, time.Now, authority)
			if wrong {
				if err == nil {
					_ = upgraded.Close()
					t.Fatal("wrong incarnation opened the source journal")
				}
				if !strings.Contains(err.Error(), "incarnation") {
					t.Fatalf("wrong rejection: %v", err)
				}
				after, readErr := os.ReadFile(path)
				if readErr != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("wrong incarnation changed the original database before rejection")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer upgraded.Close()
			if err := upgraded.QuarantineBoundedRecovery(t.Context(), authority); err != nil {
				t.Fatal(err)
			}
			identity, err := upgraded.BoundedDataIdentity(t.Context())
			if err != nil || identity.Version != 17 {
				t.Fatalf("journal15 did not migrate to17: %+v %v", identity, err)
			}
			run, err := upgraded.Run(t.Context(), as.RunID)
			if err != nil || !run.Held || !run.Fenced || run.StoppedConfirmed {
				t.Fatalf("migration changed unknown work: %+v %v", run, err)
			}
			pending, err := upgraded.Requests(t.Context())
			if err != nil || len(pending) != 1 || pending[0].Envelope.RequestID != request.Envelope.RequestID {
				t.Fatalf("migration lost original request: %+v %v", pending, err)
			}
		})
	}
}
