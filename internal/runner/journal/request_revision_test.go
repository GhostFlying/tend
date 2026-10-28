package journal

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestRequestControlRevisionSurvivesReopen(t *testing.T) {
	for _, viaFinish := range []bool{false, true} {
		name := "put"
		if viaFinish {
			name = "finish"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "journal.db")
			j := open(t, path)
			var originals []Request
			for i, value := range []int64{0, 0, 9007199254740993} {
				id := []string{"absent", "zero", "nonzero"}[i]
				r := Request{RunID: "run_" + id, Envelope: protocol.Envelope{
					RequestID: "req_" + id, Operation: protocol.OpRunsSubmitArtifact,
					Target:  protocol.Target{Type: "run", ID: "run_" + id},
					Payload: json.RawMessage(`{"artifact_id":"artifact"}`),
				}}
				if i > 0 {
					r.Envelope.ExpectedControlRevision = &value
				}
				if viaFinish {
					if err := j.AcceptAssignment(t.Context(), protocol.Assignment{RunID: r.RunID}, ""); err != nil {
						t.Fatal(err)
					}
					if err := j.Finish(t.Context(), r.RunID, PhaseSubmitting, r); err != nil {
						t.Fatal(err)
					}
				} else if err := j.PutRequest(t.Context(), r); err != nil {
					t.Fatal(err)
				}
				originals = append(originals, r)
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			j = open(t, path)
			defer j.Close()
			got, err := j.Requests(t.Context())
			if err != nil || len(got) != len(originals) {
				t.Fatalf("requests after reopen = %+v, %v", got, err)
			}
			for _, want := range originals {
				restored, ok, err := j.RequestForRun(t.Context(), want.RunID)
				if err != nil || !ok {
					t.Fatalf("restore %s: found=%v, err=%v", want.RunID, ok, err)
				}
				assertRequestIntent(t, restored, want)
			}
			// Check the list retains distinct optional values, too.
			for _, restored := range got {
				for _, want := range originals {
					if restored.Envelope.RequestID == want.Envelope.RequestID {
						assertRequestIntent(t, restored, want)
					}
				}
			}
		})
	}
}

func TestJournalTenMigrationPreservesAbsentRequestRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries[:10] {
		body, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), string(body)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(t.Context(), `PRAGMA user_version=10;
INSERT INTO requests(request_id,operation,run_id,target_json,payload_json,created_at)
VALUES('req_legacy','runs.submit_artifact','run_legacy','{"type":"run","id":"run_legacy"}','{"artifact_id":"artifact"}',123)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	want := Request{RunID: "run_legacy", CreatedAtMillis: 123, Envelope: protocol.Envelope{
		RequestID: "req_legacy", Operation: protocol.OpRunsSubmitArtifact,
		Target:  protocol.Target{Type: "run", ID: "run_legacy"},
		Payload: json.RawMessage(`{"artifact_id":"artifact"}`),
	}}
	for i := 0; i < 2; i++ {
		j := open(t, path)
		var version int64
		if err := j.db.QueryRowContext(t.Context(), `PRAGMA user_version`).Scan(&version); err != nil || version != CurrentVersion {
			t.Fatalf("schema after open = %d, %v", version, err)
		}
		got, err := j.Requests(t.Context())
		if err != nil || len(got) != 1 {
			t.Fatalf("legacy requests = %+v, %v", got, err)
		}
		assertRequestIntent(t, got[0], want)
		if got[0].CreatedAtMillis != want.CreatedAtMillis {
			t.Fatalf("legacy timestamp changed: %d", got[0].CreatedAtMillis)
		}
		if err := j.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func assertRequestIntent(t *testing.T, got, want Request) {
	t.Helper()
	if got.Envelope.RequestID != want.Envelope.RequestID || got.RunID != want.RunID {
		t.Fatalf("request identity changed: got=%+v want=%+v", got, want)
	}
	gotRevision, wantRevision := got.Envelope.ExpectedControlRevision, want.Envelope.ExpectedControlRevision
	if (gotRevision == nil) != (wantRevision == nil) || (gotRevision != nil && *gotRevision != *wantRevision) {
		t.Errorf("%s revision = %v, want %v", want.Envelope.RequestID, gotRevision, wantRevision)
	}
	gotHash, err := protocol.PayloadHash(&got.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	wantHash, err := protocol.PayloadHash(&want.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	if gotHash != wantHash {
		t.Errorf("%s same-request intent hash changed: got=%s want=%s", want.Envelope.RequestID, gotHash, wantHash)
	}
}
