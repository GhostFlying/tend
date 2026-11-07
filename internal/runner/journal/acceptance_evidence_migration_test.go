package journal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// Opt-in uses an already published legacy runner against only a disposable
// journal and dummy credential. It never connects to a deployed service.
func TestAcceptanceEvidenceLegacyWriterRejectsJournal18BeforeBusinessRead(t *testing.T) {
	legacy := os.Getenv("TEND_TEST_OLD_RUNNER")
	if legacy == "" {
		t.Skip("opt-in: exact legacy runner artifact required")
	}
	if !filepath.IsAbs(legacy) {
		t.Fatal("legacy runner artifact must be absolute")
	}
	home := t.TempDir()
	state := filepath.Join(home, "runner")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, "journal.db")
	j := open(t, path)
	as := protocol.Assignment{RunID: "run_original", TaskID: "task_original", Role: protocol.RoleAccept, AcceptanceEvidenceFiles: true}
	if err := j.AcceptAssignment(t.Context(), as, ""); err != nil {
		t.Fatal(err)
	}
	if err := j.SetThread(t.Context(), as.RunID, "original-native-binding"); err != nil {
		t.Fatal(err)
	}
	request := Request{RunID: as.RunID, Envelope: protocol.Envelope{RequestID: "original-file-verdict-request", Operation: protocol.OpAcceptancesSubmit, Target: protocol.Target{Type: "run", ID: as.RunID}, Payload: json.RawMessage(`{"verdict":"blocked","evidence":{"native_outcome":"unknown"},"files":[{"file_id":"original-file","name":"original.log","size":8,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`)}}
	if err := j.PutRequest(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	authority := protocol.BoundedMaintenanceAuthorization{RequestID: "original-quarantine", Role: "runner"}
	bindBoundedTestAuthority(t, j, &authority)
	if err := j.QuarantineBoundedRecovery(t.Context(), authority); err != nil {
		t.Fatal(err)
	}
	beforeRun, _ := j.Run(t.Context(), as.RunID)
	beforeRequests, _ := j.Requests(t.Context())
	beforeQuarantine, _ := j.BoundedRecovery(t.Context())
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("legacy writer reached business HTTP before rejecting journal18")
	}))
	defer server.Close()
	cfg, _ := json.Marshal(map[string]any{"home": home, "server_url": server.URL, "host_id": "fixture"})
	config := filepath.Join(home, "runner.json")
	if err := os.WriteFile(config, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, legacy, "--config", config)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "TEND_HOST_TOKEN=fixture-only"}
	output, runErr := command.CombinedOutput()
	if runErr == nil || ctx.Err() != nil || !strings.Contains(string(output), fmt.Sprintf("journal: version %d is newer than this runner (17)", CurrentVersion)) {
		t.Fatalf("legacy writer did not reject at its original schema guard: %v %s", runErr, output)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("legacy reader rewrote journal18 before rejection", err)
	}
	j = open(t, path)
	defer j.Close()
	gotRun, _ := j.Run(t.Context(), as.RunID)
	gotRequests, _ := j.Requests(t.Context())
	gotQuarantine, _ := j.BoundedRecovery(t.Context())
	if !reflect.DeepEqual(gotRun, beforeRun) || !reflect.DeepEqual(gotRequests, beforeRequests) || !reflect.DeepEqual(gotQuarantine, beforeQuarantine) {
		t.Fatal("legacy startup changed original pending envelope, binding or quarantine")
	}
}

func TestAcceptanceEvidenceWriterMigrationPreservesHistoricalPendingWork(t *testing.T) {
	for _, version := range []int{15, 16, 17, 18} {
		t.Run(fmt.Sprintf("journal%d", version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "journal.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			names, err := migrationFS.ReadDir("migrations")
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range names[:version] {
				body, err := migrationFS.ReadFile("migrations/" + entry.Name())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(t.Context(), string(body)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.ExecContext(t.Context(), fmt.Sprintf("PRAGMA user_version=%d", version)); err != nil {
				t.Fatal(err)
			}
			old := &Journal{path: path, db: db, now: func() time.Time { return time.UnixMilli(1000) }}
			as := protocol.Assignment{RunID: "run_original", TaskID: "task_original", LeaseID: "lease_original", Role: protocol.RoleAccept, AcceptanceEvidenceFiles: version >= 18}
			if err := old.AcceptAssignment(t.Context(), as, ""); err != nil {
				t.Fatal(err)
			}
			if err := old.SetThread(t.Context(), as.RunID, `{"run":"original","native_binding":"opaque-original-turn"}`); err != nil {
				t.Fatal(err)
			}
			if err := old.SetHeld(t.Context(), as.RunID, true); err != nil {
				t.Fatal(err)
			}
			revision := int64(7)
			request := Request{RunID: as.RunID, Envelope: protocol.Envelope{RequestID: "req_original_unknown", Operation: protocol.OpAcceptancesSubmit, Target: protocol.Target{Type: "run", ID: as.RunID}, ExpectedControlRevision: &revision, Payload: json.RawMessage(`{"verdict":"blocked","evidence":{"native_outcome":"unknown"}}`)}}
			if err := old.PutRequest(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			var authority, snapshot string
			if version >= 17 {
				authority = `{"request_id":"original-quarantine","role":"runner","reason":"original unknown native work"}`
				snapshot = `{"runs":[{"native_binding":"opaque-original-turn"}],"requests":[{"request_id":"req_original_unknown"}]}`
				if _, err := db.ExecContext(t.Context(), `INSERT INTO bounded_recovery_quarantines(request_id,authority_json,snapshot_json,active) VALUES(?,?,?,1)`, "original-quarantine", authority, snapshot); err != nil {
					t.Fatal(err)
				}
			}
			// Read the original schema directly; the current Runs reader requires
			// the new declaration-receipt table, which does not exist yet.
			var before string
			const frozen = `SELECT json_array(assignment_json,thread_id,held,attempts,fenced,stopped_confirmed,worker_kind) FROM runs WHERE run_id=?`
			if err := db.QueryRowContext(t.Context(), frozen, as.RunID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			requests, err := old.Requests(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err := old.Close(); err != nil {
				t.Fatal(err)
			}
			current, err := Open(t.Context(), path, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer current.Close()
			var installed int64
			if err := current.db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&installed); err != nil || installed != CurrentVersion {
				t.Fatal("acceptance evidence writer barrier was not installed", installed, err)
			}
			var after string
			if err := current.db.QueryRowContext(t.Context(), frozen, as.RunID).Scan(&after); err != nil || before != after {
				t.Fatal("migration rewrote the original assignment/native binding", after, err)
			}
			afterRequests, err := current.Requests(t.Context())
			if err != nil || !reflect.DeepEqual(requests, afterRequests) {
				t.Fatal("migration changed the pending original envelope", afterRequests, err)
			}
			if version >= 17 {
				var gotAuthority, gotSnapshot string
				if err := current.db.QueryRowContext(t.Context(), `SELECT authority_json,snapshot_json FROM bounded_recovery_quarantines WHERE request_id=? AND active=1`, "original-quarantine").Scan(&gotAuthority, &gotSnapshot); err != nil || gotAuthority != authority || gotSnapshot != snapshot {
					t.Fatal("migration changed the original quarantine", gotAuthority, gotSnapshot, err)
				}
			}
		})
	}
}
