package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func acceptanceFileFixture(t *testing.T, s *Store) {
	t.Helper()
	insertTask(t, s, "evidence-task")
	for _, role := range []string{"implement", "accept"} {
		mustExec(t, s, `INSERT INTO plan_actions(action_id,task_id,kind,effect_key,trigger_json,target_spec_version,authorization_json,generation,status,created_request_id,created_at,updated_at) VALUES(?, 'evidence-task', ?, ?, '{}',1,'{}',1,'done','original-request',7,8)`, role+"-action", role, role+"-effect")
		mustExec(t, s, `INSERT INTO runs(run_id,task_id,action_id,generation,role,desired_state,observed_state,created_at,updated_at) VALUES(?,'evidence-task',?,1,?,'run','finished',7,8)`, role+"-run", role+"-action", role)
	}
	mustExec(t, s, `INSERT INTO artifacts(artifact_id,task_id,run_id,kind,ref_json,digest,created_at) VALUES('evidence-artifact','evidence-task','implement-run','result','{}','original-digest',9)`)
	mustExec(t, s, `INSERT INTO acceptances(acceptance_id,task_id,run_id,spec_version,artifact_id,verdict,evidence_json,issues_json,created_at) VALUES('evidence-acceptance','evidence-task','accept-run',1,'evidence-artifact','blocked','{"path":"old-scratch/log.txt"}','[]',10)`)
}

func TestAcceptanceEvidenceFilesMigrateWithoutReinterpretingOldEvidence(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "ledger.db")
	db, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	old := &Store{write: db, read: db, now: testkit.NewClock().Now}
	if err := old.migrate(ctx, 94); err != nil {
		t.Fatal(err)
	}
	acceptanceFileFixture(t, old)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path, testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := ListAcceptances(ctx, s.read, "evidence-task", "")
	if err != nil || len(rows) != 1 || rows[0].FilesJSON != "[]" || rows[0].EvidenceJSON != `{"path":"old-scratch/log.txt"}` {
		t.Fatalf("legacy evidence changed: %+v, %v", rows, err)
	}
	var contracts int
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_acceptance_evidence_contracts`).Scan(&contracts); err != nil || contracts != 0 {
		t.Fatal("migration granted old runs new file authority", contracts, err)
	}
}

func TestAcceptanceEvidenceFinalizationSurvivesReopenAndRequiresExactManifest(t *testing.T) {
	ctx := t.Context()
	s, path := openFreshTest(t)
	acceptanceFileFixture(t, s)
	original := []byte("full independent check output\n")
	sum := sha256.Sum256(original)
	f := protocol.ArtifactFile{FileID: "acceptance-file", Name: "check.log", Size: int64(len(original)), SHA256: hex.EncodeToString(sum[:])}
	if err := s.Write(ctx, func(tx *Tx) error {
		return tx.InsertDeliverableFile(ctx, DeliverableFileRow{ArtifactFile: f, TaskID: "evidence-task", RunID: "accept-run", Content: original, CreatedAt: 10})
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := GetFinalizedAcceptanceFile(ctx, s.read, f.FileID); !errors.Is(err, ErrNotFound) {
		t.Fatal("unreferenced upload became finalized evidence", err)
	}
	for _, field := range []string{"name", "size", "sha256", "file_id"} {
		bad := f
		switch field {
		case "name":
			bad.Name = "other.log"
		case "size":
			bad.Size++
		case "sha256":
			bad.SHA256 = "wrong"
		case "file_id":
			bad.FileID = "wrong"
		}
		raw, _ := json.Marshal([]protocol.ArtifactFile{bad})
		if err := s.Write(ctx, func(tx *Tx) error { return tx.SetAcceptanceFiles(ctx, "evidence-acceptance", string(raw)) }); err != nil {
			t.Fatal(err)
		}
		if _, _, err := GetFinalizedAcceptanceFile(ctx, s.read, f.FileID); !errors.Is(err, ErrNotFound) {
			t.Fatal("mismatched "+field+" granted evidence membership", err)
		}
	}
	raw, _ := json.Marshal([]protocol.ArtifactFile{f})
	if err := s.Write(ctx, func(tx *Tx) error { return tx.SetAcceptanceFiles(ctx, "evidence-acceptance", string(raw)) }); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path, testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	file, acceptance, err := GetFinalizedAcceptanceFile(ctx, s.read, f.FileID)
	if err != nil || file.ArtifactFile != f || acceptance.AcceptanceID != "evidence-acceptance" || acceptance.Verdict != protocol.VerdictBlocked {
		t.Fatalf("original acceptance provenance lost: %+v %+v %v", file, acceptance, err)
	}
	window, err := ReadFinalizedDeliverableFile(context.Background(), s.read, f.FileID, 0, 65536, s.FileStore())
	if err != nil || !bytes.Equal(window.Content, original) {
		t.Fatalf("original evidence bytes unavailable after reopen: %q %v", window.Content, err)
	}
}
