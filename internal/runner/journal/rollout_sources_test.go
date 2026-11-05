package journal

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestRolloutLocatorSurvivesEventCleanupReopenAndRunCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j := open(t, path)
	ctx := t.Context()
	as := protocol.Assignment{RunID: "run", Role: protocol.RoleImplement}
	if err := j.AcceptAssignment(ctx, as, ""); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		if err := j.RegisterRollout(ctx, "deployment", "run", protocol.RolloutPreviewSource{SessionID: id, TurnID: "turn", Path: filepath.Join(t.TempDir(), id)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.SetPhase(ctx, "run", PhaseDone); err != nil {
		t.Fatal(err)
	}
	if err := j.AckEvents(ctx, "run", 100); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	j = open(t, path)
	defer j.Close()
	sources, err := j.RolloutSources(ctx, "deployment", "run")
	if err != nil || len(sources) != 2 || sources[0].SessionID != "first" || sources[1].SessionID != "second" {
		t.Fatal(sources, err)
	}
	if err = j.RegisterRollout(ctx, "another-deployment", "run", sources[0]); err == nil {
		t.Fatal("cross-deployment adoption accepted")
	}
	if err = j.RegisterRollout(ctx, "another-deployment", "another-run", protocol.RolloutPreviewSource{SessionID: "first", TurnID: "different-turn"}); err == nil {
		t.Fatal("cross-deployment session adoption accepted with a different turn")
	}
	if err = j.RegisterRollout(ctx, "deployment", "another-run", sources[0]); err == nil {
		t.Fatal("cross-run turn adoption accepted")
	}
	if err = j.RegisterRollout(ctx, "deployment", "another-run", protocol.RolloutPreviewSource{SessionID: "first", TurnID: "new-turn"}); err != nil {
		t.Fatal("manager session reuse was rejected", err)
	}
}

func TestCleanupPreservesRegisteredOriginalAndArtifacts(t *testing.T) {
	root := t.TempDir()
	j := open(t, filepath.Join(root, "journal.db"))
	defer j.Close()
	path := filepath.Join(root, "native", "rollout-session.jsonl")
	if err := j.RegisterRollout(t.Context(), "deployment", "run", protocol.RolloutPreviewSource{SessionID: "session", Path: path}); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{root, filepath.Dir(path), path, filepath.Join(root, "native", "rollout-session.artifacts"), filepath.Join(root, "native", "rollout-session.artifacts", "tool-results")} {
		if err := j.ProtectRollouts(t.Context(), directory); err == nil {
			t.Fatal("cleanup accepted", directory)
		}
	}
	if err := j.ProtectRollouts(t.Context(), filepath.Join(root, "unrelated")); err != nil {
		t.Fatal(err)
	}
}

func TestRolloutMigrationPreservesVersion14NativeUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	// Build the committed historical schema directly. Downgrading a current
	// database version would leave tables introduced after version 14.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries[:14] {
		body, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), string(body)); err != nil {
			t.Fatal(err)
		}
	}
	// A historical fixture must not pre-create capability or retained tables.
	var futureTables int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('capability_verifications','run_capability_receipts','retained_run_resumes')`).Scan(&futureTables); err != nil || futureTables != 0 {
		t.Fatal("version-14 fixture contains future tables", futureTables, err)
	}
	source := UsageSource{ID: "usage", RunID: "run", Coverage: "registered", Cursors: map[string]nativeusage.Cursor{"original": {Offset: 42}}}
	state, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO native_usage_sources(source_id,run_id,state_json) VALUES(?,?,?)`, source.ID, source.RunID, string(state)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "PRAGMA user_version=14"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	j := open(t, path)
	defer j.Close()
	var version int64
	if err := j.db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil || version != CurrentVersion {
		t.Fatal("historical journal did not migrate to the current version", version, err)
	}
	sources, err := j.UsageSources(t.Context())
	if err != nil || len(sources) != 1 || sources[0].Cursors["original"].Offset != 42 {
		t.Fatal(sources, err)
	}
	if err := j.RegisterRollout(t.Context(), "deployment", "run", protocol.RolloutPreviewSource{SessionID: "session", TurnID: "turn"}); err != nil {
		t.Fatal(err)
	}
	retained, err := j.RolloutSources(t.Context(), "deployment", "run")
	if err != nil || len(retained) != 1 || retained[0].SessionID != "session" {
		t.Fatal(retained, err)
	}
}
