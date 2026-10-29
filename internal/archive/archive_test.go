package archive_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/archive"
	"github.com/GhostFlying/tend/internal/protocol"
	_ "modernc.org/sqlite"
)

func fixture(t *testing.T, version int) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ledger ?#%.db")
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "wal_autocheckpoint(0)")
	q.Add("_pragma", "foreign_keys(1)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at INTEGER NOT NULL) STRICT`)
	entries, err := os.ReadDir("../store/migrations")
	if err != nil {
		t.Fatal(err)
	}
	for i, entry := range entries {
		if i >= version {
			break
		}
		body, err := os.ReadFile(filepath.Join("../store/migrations", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, db, string(body))
		mustExec(t, db, `INSERT INTO schema_migrations VALUES(?,?,0)`, i+1, strings.TrimSuffix(entry.Name()[5:], ".sql"))
	}
	mustExec(t, db, `INSERT INTO deployment_identity VALUES(1,'old-ledger')`)
	mustExec(t, db, `INSERT INTO conversation_messages(message_id,author,body,created_at) VALUES('message-last','owner','last committed WAL message',1)`)
	return path, db
}
func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(query, err)
	}
}
func hashTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		bytes, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(bytes)
		relative, _ := filepath.Rel(root, path)
		out[relative] = hex.EncodeToString(hash[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func capture(t *testing.T, path string) (string, archive.Manifest) {
	t.Helper()
	destination := filepath.Join(t.TempDir(), "archive")
	manifest, err := archive.Capture(t.Context(), archive.CaptureOptions{Database: path, Destination: destination, WritersStopped: true, CapturedAt: time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	return destination, manifest
}

func TestWALCaptureEverySupportedSchemaAndReaderIsUnchanged(t *testing.T) {
	for version := 53; version <= 57; version++ {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path, db := fixture(t, version)
			// Writer is quiescent but its final committed pages deliberately remain in
			// WAL. No SQLite connection is opened against this original by capture.
			if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
				t.Fatal("fixture did not retain WAL", err)
			}
			before := hashTree(t, filepath.Dir(path))
			destination, manifest := capture(t, path)
			if manifest.Schema != version || manifest.DeploymentID != "old-ledger" {
				t.Fatal(manifest)
			}
			if !reflect.DeepEqual(before, hashTree(t, filepath.Dir(path))) {
				t.Fatal("capture modified original DB/WAL/SHM")
			}
			reader, err := archive.Open(t.Context(), destination)
			if err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"work_item", "task", "run", "artifact", "acceptance", "decision", "message", "agent_conversation"} {
				if _, err = reader.Records(t.Context(), kind, "", 100); err != nil {
					t.Fatal("fixed historical adapter", kind, err)
				}
			}
			if version >= 54 {
				if _, err = reader.Records(t.Context(), "issue_control", "", 100); err != nil {
					t.Fatal(err)
				}
			}
			if version >= 57 {
				if _, err = reader.Records(t.Context(), "run_execution", "", 100); err != nil {
					t.Fatal(err)
				}
			}
			archivedBefore := hashTree(t, destination)
			record, err := reader.Record(t.Context(), "message", "message-last")
			if err != nil || record["body"] != "last committed WAL message" {
				t.Fatal("last WAL commit missing", record, err)
			}
			list, err := reader.Records(t.Context(), "message", "", 1)
			if err != nil || len(list) != 1 {
				t.Fatal(list, err)
			}
			if err = reader.Close(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(archivedBefore, hashTree(t, destination)) {
				t.Fatal("read created WAL/SHM/cache or changed archive")
			}
			// The original connection still sees exactly its final data.
			var body string
			if err = db.QueryRow(`SELECT body FROM conversation_messages WHERE message_id='message-last'`).Scan(&body); err != nil || body != record["body"] {
				t.Fatal(body, err)
			}
		})
	}
}

func TestOriginalsPreserveInlineAndExternalBytesAndOpaqueIDs(t *testing.T) {
	path, db := fixture(t, 57)
	external := []byte("external original\x00\xff")
	sum := sha256.Sum256(external)
	hash := hex.EncodeToString(sum[:])
	bucket := filepath.Join(path+".files", hash[:2])
	if err := os.MkdirAll(bucket, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bucket, hash), external, 0o600); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO conversation_attachments VALUES('attachment-original','../../not-a-path.bin','application/octet-stream',?,?,1)`, len(external), hash)
	mustExec(t, db, `INSERT INTO conversation_message_attachments VALUES('message-last','attachment-original',0)`)
	mustExec(t, db, `INSERT INTO conversation_images(image_id,message_id,position,name,media_type,data) VALUES('empty-image','message-last',1,'empty.bin','application/octet-stream',?)`, []byte{})
	image := []byte("GIF89a inline image")
	mustExec(t, db, `INSERT INTO conversation_images(image_id,message_id,position,name,media_type,data) VALUES('old-image','message-last',0,'old.gif','image/gif',?)`, image)
	mustExec(t, db, `INSERT INTO tasks(task_id,title,lifecycle,control_mode,control_revision,spec_version,priority,block_reason,created_at,updated_at) VALUES('task-old','historical','queued','managed',1,1,0,'none',1,1)`)
	mustExec(t, db, `INSERT INTO task_specs VALUES('task-old',1,'{"goal":"historical"}',NULL,1)`)
	mustExec(t, db, `INSERT INTO plan_actions(action_id,task_id,kind,effect_key,trigger_json,target_spec_version,authorization_json,generation,status,created_request_id,created_at,updated_at) VALUES('action-old','task-old','implement','old-effect','{}',1,'{}',1,'done','request-old',1,1)`)
	mustExec(t, db, `INSERT INTO runs(run_id,task_id,action_id,generation,role,desired_state,observed_state,created_at,updated_at) VALUES('run-old','task-old','action-old',1,'implement','stop','finished',1,1)`)
	report := []byte("full report file\x00")
	reportHash := sha256.Sum256(report)
	mustExec(t, db, `INSERT INTO deliverable_files(file_id,task_id,run_id,name,size,sha256,content,created_at) VALUES('report-old','task-old','run-old','report.bin',?,?,?,1)`, len(report), hex.EncodeToString(reportHash[:]), report)
	destination, manifest := capture(t, path)
	reader, err := archive.Open(t.Context(), destination)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for _, test := range []struct {
		kind, id string
		bytes    []byte
	}{{"attachment", "attachment-original", external}, {"image", "old-image", image}, {"deliverable", "report-old", report}, {"image", "empty-image", []byte{}}} {
		metadata, file, err := reader.OpenOriginal(test.kind, test.id)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(file)
		_ = file.Close()
		if err != nil || !reflect.DeepEqual(got, test.bytes) || metadata.Size != int64(len(test.bytes)) {
			t.Fatal(metadata, got, err)
		}
	}
	if _, _, err := reader.OpenOriginal("attachment", "../snapshot.sqlite"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("arbitrary path accepted", err)
	}
	found := false
	for _, resource := range manifest.Resources {
		if resource.RunID == "run-old" && resource.Kind == "native_history" && resource.Status == "not_collected" {
			found = true
		}
	}
	if !found {
		t.Fatal("native coverage gap omitted")
	}
}

func TestUnsupportedAndInvalidArchivesNeverModifySource(t *testing.T) {
	path, db := fixture(t, 57)
	mustExec(t, db, `CREATE TABLE future_rows(value TEXT)`)
	mustExec(t, db, `INSERT INTO schema_migrations VALUES(58,'future',1)`)
	before := hashTree(t, filepath.Dir(path))
	destination := filepath.Join(t.TempDir(), "unsupported")
	_, err := archive.Capture(t.Context(), archive.CaptureOptions{Database: path, Destination: destination, WritersStopped: true, CapturedAt: time.Now()})
	if !errors.Is(err, archive.ErrUnsupportedSchema) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, hashTree(t, filepath.Dir(path))) {
		t.Fatal("unsupported source changed")
	}
	if _, err = os.Stat(filepath.Join(destination, "raw", "ledger.db-wal")); err != nil {
		t.Fatal("unsupported raw evidence not retained", err)
	}
	if _, err = archive.Open(t.Context(), destination); err == nil {
		t.Fatal("partial archive accepted")
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err = archive.Open(t.Context(), missing); err == nil {
		t.Fatal("missing archive accepted")
	}
	if _, err = os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("reader created missing root")
	}
}

func TestVerifyDetectsPayloadLossAndTraversal(t *testing.T) {
	path, _ := fixture(t, 57)
	root, manifest := capture(t, path)
	if err := os.WriteFile(filepath.Join(root, "snapshot.sqlite"), []byte("truncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Verify(t.Context(), root); err == nil {
		t.Fatal("corrupt snapshot accepted")
	}
	manifest.Files[0].Path = "../outside"
	body, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Verify(context.Background(), root); err == nil {
		t.Fatal("manifest escape accepted")
	}
}

func TestSelectedResourcesPreserveJournalWALAndNativeLineage(t *testing.T) {
	for version := 10; version <= 12; version++ {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path, db := fixture(t, 57)
			mustExec(t, db, `INSERT INTO agent_thread_history VALUES('hosted-thread','main',1)`)
			journalPath := filepath.Join(t.TempDir(), "journal.db")
			u := url.URL{Scheme: "file", Path: journalPath}
			u.RawQuery = "_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(0)"
			j, err := sql.Open("sqlite", u.String())
			if err != nil {
				t.Fatal(err)
			}
			j.SetMaxOpenConns(1)
			defer j.Close()
			migrations, err := os.ReadDir("../runner/journal/migrations")
			if err != nil {
				t.Fatal(err)
			}
			for i, m := range migrations {
				if i >= version {
					break
				}
				body, err := os.ReadFile(filepath.Join("../runner/journal/migrations", m.Name()))
				if err != nil {
					t.Fatal(err)
				}
				mustExec(t, j, string(body))
				mustExec(t, j, fmt.Sprintf("PRAGMA user_version=%d", i+1))
			}
			mustExec(t, j, `INSERT INTO requests(request_id,operation,target_json,payload_json,created_at) VALUES('original-request','runner.register_inventory','{}','{"original":"bytes"}',1)`)
			if version >= 11 {
				mustExec(t, j, `UPDATE requests SET expected_control_revision=0`)
			}
			native := filepath.Join(t.TempDir(), "native.jsonl")
			body := []byte("{\"thread\":\"child-thread\",\"parent\":\"parent-thread\"}\n")
			if err = os.WriteFile(native, body, 0o600); err != nil {
				t.Fatal(err)
			}
			before := hashTree(t, filepath.Dir(journalPath))
			sourceBefore := hashTree(t, filepath.Dir(path))
			destination := filepath.Join(t.TempDir(), "archive")
			manifest, err := archive.Capture(t.Context(), archive.CaptureOptions{Database: path, Destination: destination, WritersStopped: true, CapturedAt: time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC), Resources: []archive.ResourceSource{
				{ID: "journal-selected", Kind: "runner_journal", HostID: "runner-a", Scope: "Stopped runner journal only", Path: journalPath, Status: "collected"},
				{ID: "native-selected", Kind: "native_history", ThreadID: "child-thread", ParentThreadID: "parent-thread", Scope: "Owner-selected child native transcript", Path: native, Status: "partial", Reason: "Parent transcript unavailable"},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, hashTree(t, filepath.Dir(journalPath))) || !reflect.DeepEqual(sourceBefore, hashTree(t, filepath.Dir(path))) {
				t.Fatal("capture changed original journal or ledger")
			}
			reader, err := archive.Open(t.Context(), destination)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			foundJournal, foundNative, foundHostedGap := false, false, false
			for _, r := range manifest.Resources {
				switch r.ID {
				case "journal-selected":
					foundJournal = true
					if r.JournalSchema != version || len(r.Files) != 1 {
						t.Fatal(r)
					}
					archived := filepath.Join(destination, filepath.FromSlash(r.Files[0]))
					u := url.URL{Scheme: "file", Path: archived}
					u.RawQuery = "mode=ro&immutable=1"
					sealed, err := sql.Open("sqlite", u.String())
					if err != nil {
						t.Fatal(err)
					}
					defer sealed.Close()
					var payload string
					if err = sealed.QueryRow(`SELECT payload_json FROM requests WHERE request_id='original-request'`).Scan(&payload); err != nil || payload != `{"original":"bytes"}` {
						t.Fatal(payload, err)
					}
					if version >= 11 {
						var revision int
						if err = sealed.QueryRow(`SELECT expected_control_revision FROM requests`).Scan(&revision); err != nil || revision != 0 {
							t.Fatal("journal CAS identity lost", revision, err)
						}
					}
					if _, err = os.Stat(archived + "-wal"); !os.IsNotExist(err) {
						t.Fatal("journal snapshot requires WAL")
					}
				case "native-selected":
					foundNative = true
					if r.ParentThreadID != "parent-thread" || r.Status != "partial" {
						t.Fatal(r)
					}
					file, err := reader.OpenResource(r.ID, 0)
					if err != nil {
						t.Fatal(err)
					}
					got, err := io.ReadAll(file)
					_ = file.Close()
					if err != nil || string(got) != string(body) {
						t.Fatal(string(got), err)
					}
				case "uncollected-thread-hosted-thread":
					foundHostedGap = r.Status == "not_collected"
				}
			}
			if !foundJournal || !foundNative || !foundHostedGap {
				t.Fatal("coverage/lineage missing", manifest.Resources)
			}
			encoded, _ := json.Marshal(manifest)
			if strings.Contains(string(encoded), native) || strings.Contains(string(encoded), journalPath) {
				t.Fatal("manifest included local source paths")
			}
		})
	}
}

func TestStaticSelectionDoesNotExpandReferencesOrCopyBusiness(t *testing.T) {
	path, db := fixture(t, 57)
	mustExec(t, db, `INSERT INTO repos(repo,remote_url,base_candidates_json,retired,created_at,updated_at) VALUES('selected','https://example.com/selected.git','["main"]',0,1,1)`)
	mustExec(t, db, `INSERT INTO repos(repo,remote_url,base_candidates_json,retired,created_at,updated_at) VALUES('unselected','https://example.com/private.git','["main"]',0,1,1)`)
	mustExec(t, db, `INSERT INTO agent_category_versions VALUES('implementer',1,'Old title','Old role','active','old-request')`)
	mustExec(t, db, `INSERT INTO agent_category_versions VALUES('implementer',2,'Current title','Current role','active','new-request')`)
	root, _ := capture(t, path)
	reader, err := archive.Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	empty, err := reader.ExportSeed(t.Context(), archive.Selection{MainSettings: true})
	if err != nil || len(empty.Repositories) != 0 || empty.MainSettings != nil {
		t.Fatal(empty, err)
	}
	seed, err := reader.ExportSeed(t.Context(), archive.Selection{Repositories: []string{"selected"}, Categories: []string{"implementer"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(seed.Repositories) != 1 || len(seed.Categories) != 1 || seed.Categories[0].Title != "Current title" {
		t.Fatal(seed)
	}
	encoded, _ := json.Marshal(seed)
	for _, forbidden := range []string{"old-request", "new-request", "message-last", "last committed WAL message", "unselected"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatal("unselected history in seed", forbidden)
		}
	}
	if _, err = reader.ExportSeed(t.Context(), archive.Selection{Repositories: []string{"selected", "selected"}}); err == nil {
		t.Fatal("ambiguous duplicate selection accepted")
	}
	if _, err = reader.ExportSeed(t.Context(), archive.Selection{Repositories: []string{"missing"}}); err == nil {
		t.Fatal("missing selected repository silently ignored")
	}
}

func TestStaticSeedPreservesGitRemoteSyntaxAndRejectsEmbeddedPasswords(t *testing.T) {
	path, db := fixture(t, 57)
	remotes := map[string]string{
		"scp":            "git@github.com:example/tend.git",
		"ssh":            "ssh://git@example.com/team/repo.git",
		"https":          "https://example.com/team/repo.git",
		"local":          "/srv/repos/team/repo.git",
		"https-password": "https://owner:embedded-password@example.com/team/repo.git",
		"https-token":    "https://old-access-token@example.com/team/repo.git",
		"http-token":     "http://old-access-token@example.com/team/repo.git",
		"ssh-password":   "ssh://git:embedded-password@example.com/team/repo.git",
	}
	for id, remote := range remotes {
		mustExec(t, db, `INSERT INTO repos(repo,remote_url,base_candidates_json,retired,created_at,updated_at) VALUES(?,?,'["main"]',0,1,1)`, id, remote)
	}
	root, _ := capture(t, path)
	reader, err := archive.Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for _, id := range []string{"scp", "ssh", "https", "local"} {
		t.Run(id, func(t *testing.T) {
			seed, err := reader.ExportSeed(t.Context(), archive.Selection{Repositories: []string{id}})
			if err != nil {
				t.Fatal("valid Git remote rejected", err)
			}
			if len(seed.Repositories) != 1 || seed.Repositories[0].RemoteURL != remotes[id] || protocol.RepositoryRemoteKey(seed.Repositories[0].RemoteURL) != protocol.RepositoryRemoteKey(remotes[id]) {
				t.Fatal("Git remote identity changed", seed)
			}
		})
	}
	for _, id := range []string{"https-password", "ssh-password", "https-token", "http-token"} {
		t.Run(id, func(t *testing.T) {
			if _, err := reader.ExportSeed(t.Context(), archive.Selection{Repositories: []string{id}}); err == nil {
				t.Fatal("embedded remote authentication copied into static seed")
			}
		})
	}
}
