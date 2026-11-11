//go:build linux

package updater

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func createRetentionCheckpoint(t *testing.T, h *harness, id string, at time.Time, verify bool) protocol.BackupCheckpoint {
	t.Helper()
	op := &Operation{BackupDir: filepath.Join(h.cfg.StateDir, "backups", id), StartedAt: at, Previous: h.s.Status().Current}
	if err := backupData(h.cfg, op); err != nil {
		t.Fatal(err)
	}
	record, err := readCheckpoint(h.cfg, op.BackupDir)
	if err != nil {
		t.Fatal(err)
	}
	if !verify {
		return record
	}
	proof := protocol.BackupRestoreProof{Format: 1, CheckpointID: record.ID, ManifestSHA256: record.ManifestSHA256, Restored: true, VerifiedAtMillis: at.UnixMilli()}
	path := filepath.Join(t.TempDir(), "restore-proof.json")
	if err := writeJSON(path, proof); err != nil {
		t.Fatal(err)
	}
	hash, err := hashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	record, err = h.s.VerifyBackup(t.Context(), protocol.BackupVerification{CheckpointID: record.ID, ExpectedGeneration: h.s.Status().Generation, ManifestSHA256: record.ManifestSHA256, RestoreProofPath: path, RestoreProofSHA256: hash})
	if err != nil {
		t.Fatal(err)
	}
	return record
}
func minimalRetention() protocol.BackupPolicy {
	p := DefaultBackupPolicy()
	p.Daily = 0
	p.Weekly = 0
	return p
}

func TestBackupObjectsSharedAcrossCheckpointsAndFixedRestoreRoots(t *testing.T) {
	h := newHarness(t)
	original := filepath.Join(h.data+".files", "archive", "original")
	if err := os.MkdirAll(filepath.Dir(original), 0700); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("immutable", 2000)
	if err := os.WriteFile(original, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	one := createRetentionCheckpoint(t, h, "one", h.now.Add(-time.Hour), false)
	two := createRetentionCheckpoint(t, h, "two", h.now, false)
	f1, f2 := one.Files[len(one.Files)-1], two.Files[len(two.Files)-1]
	if f1.ObjectSHA256 == "" || f1.ObjectSHA256 != f2.ObjectSHA256 {
		t.Fatal("original copied per checkpoint", f1, f2)
	}
	if _, err := os.Stat(filepath.Join(one.Directory, f1.Name)); !os.IsNotExist(err) {
		t.Fatal("shared original copied into checkpoint", err)
	}
	if err := os.Remove(original); err != nil {
		t.Fatal(err)
	}
	op := &Operation{BackupDir: one.Directory, Backups: one.Files}
	if err := restoreData(h.cfg, op); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(original)
	if err != nil || string(restored) != body {
		t.Fatal("original missing after restore", err)
	}
	op.Backups = append([]BackupFile(nil), one.Files...)
	op.Backups[len(op.Backups)-1].Path = filepath.Join(t.TempDir(), "escape")
	if err := restoreData(h.cfg, op); err == nil {
		t.Fatal("restore escaped configured roots")
	}
	roots, err := RetainedBackupOriginals(h.cfg)
	if err != nil || len(roots) != 2 {
		t.Fatal("backup root union incomplete", roots, err)
	}
}

func TestBackupRetentionProtectionManifestCASAndSharedObjectUnion(t *testing.T) {
	h := newHarness(t)
	h.now = time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	unique := filepath.Join(h.data+".files", "old-original")
	if err := os.MkdirAll(filepath.Dir(unique), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unique, []byte("old-only-original"), 0600); err != nil {
		t.Fatal(err)
	}
	old := createRetentionCheckpoint(t, h, "old", h.now.Add(-40*24*time.Hour), true)
	if err := os.Remove(unique); err != nil {
		t.Fatal(err)
	}
	createRetentionCheckpoint(t, h, "recent1", h.now.Add(-24*time.Hour), true)
	createRetentionCheckpoint(t, h, "recent2", h.now, true)
	preview, err := h.s.BackupPreview(t.Context(), minimalRetention())
	if err != nil {
		t.Fatal(err)
	}
	deleted := 0
	for _, r := range preview.Checkpoints {
		if r.Decision == "delete" {
			deleted++
			if r.ID != old.ID {
				t.Fatal("deleted recent restore point", r)
			}
		}
	}
	if deleted != 1 || len(preview.Objects) != 1 || preview.Objects[0].Decision != "delete" {
		t.Fatal("wrong retention union", preview)
	}
	// A real new checkpoint changes the exact frozen candidate inventory.
	createRetentionCheckpoint(t, h, "unknown", h.now, false)
	if _, err := h.s.PruneBackups(t.Context(), protocol.BackupPruneRequest{Preview: preview}); err == nil {
		t.Fatal("stale preview accepted")
	}
	if err := os.WriteFile(filepath.Join(h.cfg.StateDir, "backups", "unknown", "files.json"), []byte("damaged-manifest"), 0600); err != nil {
		t.Fatal(err)
	}
	protected, err := h.s.BackupPreview(t.Context(), minimalRetention())
	if err != nil {
		t.Fatal(err)
	}
	if protected.Objects[0].Decision != "protected" {
		t.Fatal("unknown backup did not protect shared objects", protected.Objects)
	}
}

func TestBackupPruneReconcilesInterruptionAndKeepsTwoRecoverablePoints(t *testing.T) {
	h := newHarness(t)
	h.now = time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	old := createRetentionCheckpoint(t, h, "old", h.now.Add(-40*24*time.Hour), true)
	createRetentionCheckpoint(t, h, "a", h.now.Add(-24*time.Hour), true)
	createRetentionCheckpoint(t, h, "b", h.now, true)
	preview, err := h.s.BackupPreview(t.Context(), minimalRetention())
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after the durable deletion intent and one file removal.
	h.s.mu.Lock()
	h.s.state.BackupPrune = &protocol.BackupPruneState{Preview: preview, Phase: "pruning", InFlight: old.ID, StartedAtMillis: h.now.UnixMilli()}
	err = h.s.save()
	h.s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(old.Directory, backupMetadataName)); err != nil {
		t.Fatal(err)
	}
	result, err := h.s.PruneBackups(t.Context(), protocol.BackupPruneRequest{Preview: preview})
	if err != nil || result.Phase != "completed" {
		t.Fatal("original partial prune could not reconcile", result, err)
	}
	if _, err := os.Stat(old.Directory); !os.IsNotExist(err) {
		t.Fatal("old checkpoint remains", err)
	}
	retry, err := h.s.PruneBackups(t.Context(), protocol.BackupPruneRequest{Preview: preview})
	if err != nil || retry.ReclaimedBytes != result.ReclaimedBytes {
		t.Fatal("repeat prune duplicated accounting", retry, err)
	}
	after, err := h.s.BackupPreview(t.Context(), minimalRetention())
	if err != nil || len(after.Checkpoints) != 2 {
		t.Fatal("lost retained restore point", after, err)
	}
}

func TestBackupPruneRejectsCorruptRetainedCheckpointAndUnknownOperations(t *testing.T) {
	for _, kind := range []string{"corrupt", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			h.now = time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
			createRetentionCheckpoint(t, h, "old", h.now.Add(-40*24*time.Hour), true)
			createRetentionCheckpoint(t, h, "a", h.now.Add(-24*time.Hour), true)
			recent := createRetentionCheckpoint(t, h, "b", h.now, true)
			p, err := h.s.BackupPreview(t.Context(), minimalRetention())
			if err != nil {
				t.Fatal(err)
			}
			if kind == "corrupt" {
				if err = os.WriteFile(filepath.Join(recent.Directory, "data-000"), []byte("damaged"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				h.s.mu.Lock()
				h.s.state.Operation = &Operation{ID: "unknown", Phase: "starting", BackupDir: filepath.Join(h.cfg.StateDir, "backups", "old")}
				err = h.s.save()
				h.s.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err = h.s.PruneBackups(context.Background(), protocol.BackupPruneRequest{Preview: p}); err == nil {
				t.Fatal("unsafe prune accepted")
			}
			if _, err = os.Stat(filepath.Join(h.cfg.StateDir, "backups", "old", "files.json")); err != nil {
				t.Fatal("old recovery point lost", err)
			}
		})
	}
}

func TestBackupControlRejectsMissingInputAndUsesPrivateRoutes(t *testing.T) {
	h := newHarness(t)
	for _, test := range []struct {
		method, path, body string
		status             int
	}{{"GET", "/backup/status", "", 200}, {"POST", "/backup/preview", "{}", 200}, {"POST", "/backup/verify", "{}", 409}, {"POST", "/backup/prune", "{}", 409}, {"POST", "/backup/preview", "{\"unknown\":true}", 400}, {"GET", "/backup/prune", "", 405}} {
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		response := httptest.NewRecorder()
		h.s.backupHandler(response, request)
		if response.Code != test.status {
			t.Fatalf("%s %s status=%d body=%s", test.method, test.path, response.Code, response.Body.String())
		}
	}
}

func TestBackupFailedExercisePreservesActualUniqueEvidenceAndCover(t *testing.T) {
	h := newHarness(t)
	h.now = time.Unix(20000000, 0)
	cover := createRetentionCheckpoint(t, h, "cover-old", h.now.Add(-40*24*time.Hour), true)
	createRetentionCheckpoint(t, h, "new-one", h.now.Add(-20*24*time.Hour), true)
	createRetentionCheckpoint(t, h, "new-two", h.now.Add(-10*24*time.Hour), true)
	failed := createRetentionCheckpoint(t, h, "failed-copy", h.now.Add(-30*24*time.Hour), false)
	evidence := filepath.Join(t.TempDir(), "failure.txt")
	body := []byte("actual explained restore mismatch retained for investigation")
	if err := os.WriteFile(evidence, body, 0600); err != nil {
		t.Fatal(err)
	}
	evidenceHash, err := hashFile(evidence)
	if err != nil {
		t.Fatal(err)
	}
	proof := protocol.BackupRestoreProof{Format: 1, CheckpointID: failed.ID, ManifestSHA256: failed.ManifestSHA256, VerifiedAtMillis: h.now.UnixMilli(), Explained: true, CoveredByCheckpointID: cover.ID, UniqueEvidenceSHA256: evidenceHash}
	path := filepath.Join(t.TempDir(), "proof.json")
	input := protocol.BackupVerification{CheckpointID: failed.ID, ExpectedGeneration: h.s.Status().Generation, ManifestSHA256: failed.ManifestSHA256, Class: "failed_drill", RestoreProofPath: path}
	if err = writeJSON(path, proof); err != nil {
		t.Fatal(err)
	}
	input.RestoreProofSHA256, _ = hashFile(path)
	if _, err = h.s.VerifyBackup(t.Context(), input); err == nil {
		t.Fatal("hash alone pretended unique evidence was retained")
	}
	proof.UniqueEvidencePath = evidence
	if err = writeJSON(path, proof); err != nil {
		t.Fatal(err)
	}
	input.RestoreProofSHA256, _ = hashFile(path)
	if _, err = h.s.VerifyBackup(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(evidence); err != nil {
		t.Fatal(err)
	}
	preserved := 0
	if err = filepath.WalkDir(filepath.Join(h.cfg.StateDir, "backup-evidence", "originals"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if string(raw) == string(body) {
			preserved++
		}
		return nil
	}); err != nil || preserved != 1 {
		t.Fatal("unique investigation evidence not preserved once", preserved, err)
	}
	preview, err := h.s.BackupPreview(t.Context(), minimalRetention())
	if err != nil {
		t.Fatal(err)
	}
	var coverDecision, failedDecision string
	for _, r := range preview.Checkpoints {
		if r.ID == cover.ID {
			coverDecision = r.Decision
		}
		if r.ID == failed.ID {
			failedDecision = r.Decision
		}
	}
	if coverDecision != "keep" || failedDecision != "delete" {
		t.Fatal("failed exercise lost its actual recovery cover", coverDecision, failedDecision)
	}
}
