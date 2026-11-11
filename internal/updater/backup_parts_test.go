//go:build linux

package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/blobstore"
	"github.com/GhostFlying/tend/internal/protocol"
)

func checkpointOriginal(t *testing.T, c protocol.BackupCheckpoint, path string) BackupFile {
	t.Helper()
	for _, f := range c.Files {
		if f.Path == path {
			return f
		}
	}
	t.Fatal("original absent from checkpoint", path)
	return BackupFile{}
}
func backupObjectBytes(t *testing.T, h *harness) (int, int64) {
	t.Helper()
	count := 0
	var total int64
	err := filepath.WalkDir(filepath.Join(h.cfg.StateDir, "backup-objects"), func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		size, _, err := regularSize(path)
		if err != nil {
			return err
		}
		count++
		total += size
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return count, total
}
func requireOriginalRestore(t *testing.T, h *harness, c protocol.BackupCheckpoint, path string, want []byte) {
	t.Helper()
	if err := restoreData(h.cfg, &Operation{BackupDir: c.Directory, Backups: c.Files}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("restored original differs", err, len(got), len(want))
	}
}

func TestBackupAppendPacksShareThreeCheckpointsAndPruneOldRoot(t *testing.T) {
	for _, suffix := range []string{".files", ".spool"} {
		t.Run(suffix, func(t *testing.T) {
			h := newHarness(t)
			h.now = time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
			s, err := blobstore.New(h.data + suffix)
			if err != nil {
				t.Fatal(err)
			}
			prefix := bytes.Repeat([]byte("backup-prefix"), 8192)
			a, err := s.AppendOriginal(t.Context(), "append-stream", prefix)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(h.data+suffix, "archives", a.Archive)
			one := createRetentionCheckpoint(t, h, "one", h.now.Add(-40*24*time.Hour), true)
			if _, err = s.AppendOriginal(t.Context(), "append-stream", []byte("tail-two")); err != nil {
				t.Fatal(err)
			}
			twoBody := append(append([]byte{}, prefix...), []byte("tail-two")...)
			two := createRetentionCheckpoint(t, h, "two", h.now.Add(-24*time.Hour), true)
			if _, err = s.AppendOriginal(t.Context(), "append-stream", []byte("tail-three")); err != nil {
				t.Fatal(err)
			}
			threeBody := append(append([]byte{}, twoBody...), []byte("tail-three")...)
			three := createRetentionCheckpoint(t, h, "three", h.now, true)
			f1, f2, f3 := checkpointOriginal(t, one, path), checkpointOriginal(t, two, path), checkpointOriginal(t, three, path)
			if f1.ObjectSHA256 == "" || len(f2.ObjectParts) != 2 || len(f3.ObjectParts) != 3 || f2.ObjectSHA256 != "" || f3.ObjectParts[0].SHA256 != f1.ObjectSHA256 {
				t.Fatal("growing pack was copied", f1, f2, f3)
			}
			count, total := backupObjectBytes(t, h)
			if count != 3 || total != int64(len(threeBody)) {
				t.Fatal("duplicate prefix bytes", count, total, len(threeBody))
			}
			t.Logf("three checkpoints: prefix=%d shared_objects=%d shared_bytes=%d current_original_bytes=%d", len(prefix), count, total, len(threeBody))
			for _, c := range []protocol.BackupCheckpoint{two, three} {
				var metadata backupMetadata
				raw, err := os.ReadFile(filepath.Join(c.Directory, backupMetadataName))
				if err != nil {
					t.Fatal(err)
				}
				if json.Unmarshal(raw, &metadata) != nil || metadata.Format != 2 {
					t.Fatal("multipart checkpoint lost format boundary")
				}
			}
			for i, c := range []protocol.BackupCheckpoint{one, two, three} {
				requireOriginalRestore(t, h, c, path, [][]byte{prefix, twoBody, threeBody}[i])
			}
			// Frozen bounded retries keep the original flat manifest, not a newly chosen parent.
			op := &Operation{BackupDir: three.Directory, StartedAt: h.now, Previous: h.s.Status().Current}
			if err = backupBoundedRunnerDataContext(t.Context(), h.cfg, op); err != nil {
				t.Fatal(err)
			}
			if digest(op.Backups) != digest(three.Files) {
				t.Fatal("retry rewrote immutable manifest")
			}
			roots, err := RetainedBackupOriginals(h.cfg)
			if err != nil || len(roots) != 3 {
				t.Fatal("missing backup-aware original roots", roots, err)
			}
			preview, err := h.s.BackupPreview(t.Context(), minimalRetention())
			if err != nil {
				t.Fatal(err)
			}
			for _, o := range preview.Objects {
				if o.Decision != "keep" {
					t.Fatal("retained part is collectible", o)
				}
			}
			var oldDelete bool
			for _, c := range preview.Checkpoints {
				if c.ID == one.ID {
					oldDelete = c.Decision == "delete"
				}
			}
			if !oldDelete {
				t.Fatal("old checkpoint not eligible", preview)
			}
			if _, err = h.s.PruneBackups(t.Context(), protocol.BackupPruneRequest{Preview: preview}); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(one.Directory); !os.IsNotExist(err) {
				t.Fatal("old checkpoint remains", err)
			}
			count, total = backupObjectBytes(t, h)
			if count != 3 || total != int64(len(threeBody)) {
				t.Fatal("prune lost shared objects", count, total)
			}
			requireOriginalRestore(t, h, two, path, twoBody)
			requireOriginalRestore(t, h, three, path, threeBody)
		})
	}
}

func TestBackupAppendPackRewriteAndShrinkCreateNewSnapshot(t *testing.T) {
	for _, kind := range []string{"rewrite", "shrink", "branch"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			s, err := blobstore.New(h.data + ".files")
			if err != nil {
				t.Fatal(err)
			}
			prefix := []byte("original immutable prefix")
			w, err := s.AppendOriginal(t.Context(), "stream", prefix)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(h.data+".files", "archives", w.Archive)
			first := createRetentionCheckpoint(t, h, "one", h.now.Add(-2*time.Hour), false)
			if _, err = s.AppendOriginal(t.Context(), "stream", []byte("old-tail")); err != nil {
				t.Fatal(err)
			}
			second := createRetentionCheckpoint(t, h, "two", h.now.Add(-time.Hour), false)
			body := []byte("rewritten immutable prefix and longer bytes")
			if kind == "shrink" {
				body = []byte("short")
			}
			if kind == "branch" {
				requireOriginalRestore(t, h, first, path, prefix)
				if _, err = s.AppendOriginal(t.Context(), "stream", []byte("new-tail")); err != nil {
					t.Fatal(err)
				}
				body = append(append([]byte{}, prefix...), []byte("new-tail")...)
			} else if err = os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			third := createRetentionCheckpoint(t, h, "three", h.now, false)
			f := checkpointOriginal(t, third, path)
			if len(f.ObjectParts) != 0 || f.ObjectSHA256 == "" {
				t.Fatal("rewritten source inferred append", f)
			}
			requireOriginalRestore(t, h, third, path, body)
			requireOriginalRestore(t, h, second, path, append(append([]byte{}, prefix...), []byte("old-tail")...))
		})
	}
}

func TestBackupPartsRejectMissingCorruptSizeAndOrderWithoutPublishing(t *testing.T) {
	for _, kind := range []string{"missing", "corrupt", "size", "order", "digest", "mixed"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			s, err := blobstore.New(h.data + ".files")
			if err != nil {
				t.Fatal(err)
			}
			w, err := s.AppendOriginal(t.Context(), "stream", []byte("first"))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(h.data+".files", "archives", w.Archive)
			createRetentionCheckpoint(t, h, "one", h.now.Add(-time.Hour), false)
			if _, err = s.AppendOriginal(t.Context(), "stream", []byte("second")); err != nil {
				t.Fatal(err)
			}
			c := createRetentionCheckpoint(t, h, "two", h.now, false)
			f := checkpointOriginal(t, c, path)
			if len(f.ObjectParts) != 2 {
				t.Fatal(f)
			}
			objects, err := backupObjectStore(h.cfg)
			if err != nil {
				t.Fatal(err)
			}
			partPath, err := objects.Path(f.ObjectParts[0].SHA256)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing":
				err = os.Remove(partPath)
			case "corrupt":
				err = os.WriteFile(partPath, []byte("wrong"), 0600)
			case "size":
				f.ObjectParts[0].Size++
			case "order":
				f.ObjectParts[0], f.ObjectParts[1] = f.ObjectParts[1], f.ObjectParts[0]
			case "digest":
				f.SHA256 = f.ObjectParts[0].SHA256
			case "mixed":
				f.ObjectSHA256 = f.SHA256
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = restoreBackupFile(t.Context(), h.cfg, c.Directory, f); err == nil {
				t.Fatal("invalid parts restored")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, before) {
				t.Fatal("failed restore published bytes", err)
			}
			if err = copyBackupContents(t.Context(), h.cfg, c.Directory, f, io.Discard); err == nil {
				t.Fatal("invalid parts verified")
			}
		})
	}
}

func TestBackupPartManifestFormatAndNestedReferencesAreProtected(t *testing.T) {
	for _, kind := range []string{"legacy_format", "future_format", "nested"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			s, err := blobstore.New(h.data + ".files")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.AppendOriginal(t.Context(), "stream", []byte("first")); err != nil {
				t.Fatal(err)
			}
			createRetentionCheckpoint(t, h, "one", h.now.Add(-time.Hour), false)
			if _, err = s.AppendOriginal(t.Context(), "stream", []byte("tail")); err != nil {
				t.Fatal(err)
			}
			c := createRetentionCheckpoint(t, h, "two", h.now, false)
			metadataPath := filepath.Join(c.Directory, backupMetadataName)
			raw, err := os.ReadFile(metadataPath)
			if err != nil {
				t.Fatal(err)
			}
			var m backupMetadata
			if err = json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			if kind == "legacy_format" {
				m.Format = 1
			} else if kind == "future_format" {
				m.Format = 3
			} else {
				manifestPath := filepath.Join(c.Directory, "files.json")
				raw, err = os.ReadFile(manifestPath)
				if err != nil {
					t.Fatal(err)
				}
				var files []map[string]any
				if err = json.Unmarshal(raw, &files); err != nil {
					t.Fatal(err)
				}
				for _, f := range files {
					if parts, ok := f["object_parts"].([]any); ok {
						parts[0].(map[string]any)["object_parts"] = []any{map[string]any{"sha256": "nested"}}
					}
				}
				if err = writeJSON(manifestPath, files); err != nil {
					t.Fatal(err)
				}
				m.Checkpoint.ManifestSHA256, err = hashFile(manifestPath)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err = writeJSON(metadataPath, m); err != nil {
				t.Fatal(err)
			}
			if _, err = readCheckpoint(h.cfg, c.Directory); err == nil {
				t.Fatal("unsupported representation accepted")
			}
			preview, err := h.s.BackupPreview(t.Context(), minimalRetention())
			if err != nil {
				t.Fatal(err)
			}
			for _, o := range preview.Objects {
				if o.Decision != "protected" {
					t.Fatal("unknown manifest did not protect objects", o)
				}
			}
		})
	}
}

func TestBackupLegacyLocalAndWholeSnapshotsRemainReadable(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(h.data+".files", "old-original")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	body := []byte("legacy local original")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	legacy := h.cfg
	legacy.StateDir = ""
	op := &Operation{BackupDir: filepath.Join(h.cfg.StateDir, "backups", "local"), StartedAt: h.now, Previous: h.s.Status().Current}
	if err := backupData(legacy, op); err != nil {
		t.Fatal(err)
	}
	if err := writeBackupMetadata(h.cfg, op); err != nil {
		t.Fatal(err)
	}
	c, err := readCheckpoint(h.cfg, op.BackupDir)
	if err != nil {
		t.Fatal(err)
	}
	if sharedBackupFile(checkpointOriginal(t, c, path)) {
		t.Fatal("legacy snapshot not local")
	}
	requireOriginalRestore(t, h, c, path, body)
	// The existing immutable-whole-object path is format1 and remains readable.
	whole := createRetentionCheckpoint(t, h, "whole", h.now.Add(time.Hour), false)
	requireOriginalRestore(t, h, whole, path, body)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := copyBackupContents(ctx, h.cfg, whole.Directory, checkpointOriginal(t, whole, path), io.Discard); err == nil {
		t.Fatal("cancelled reader succeeded")
	}
}

func TestBackupPartDamageBlocksPruneBeforeDeletingCheckpoint(t *testing.T) {
	for _, kind := range []string{"missing", "corrupt"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			h.now = time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
			s, err := blobstore.New(h.data + ".files")
			if err != nil {
				t.Fatal(err)
			}
			w, err := s.AppendOriginal(t.Context(), "stream", []byte("prefix"))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(h.data+".files", "archives", w.Archive)
			one := createRetentionCheckpoint(t, h, "one", h.now.Add(-40*24*time.Hour), true)
			if _, err = s.AppendOriginal(t.Context(), "stream", []byte("tail")); err != nil {
				t.Fatal(err)
			}
			two := createRetentionCheckpoint(t, h, "two", h.now.Add(-24*time.Hour), true)
			if _, err = s.AppendOriginal(t.Context(), "stream", []byte("later")); err != nil {
				t.Fatal(err)
			}
			createRetentionCheckpoint(t, h, "three", h.now, true)
			f := checkpointOriginal(t, two, path)
			store, err := backupObjectStore(h.cfg)
			if err != nil {
				t.Fatal(err)
			}
			part, err := store.Path(f.ObjectParts[1].SHA256)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "missing" {
				err = os.Remove(part)
			} else {
				err = os.WriteFile(part, []byte("evil"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			preview, err := h.s.BackupPreview(t.Context(), minimalRetention())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = h.s.PruneBackups(t.Context(), protocol.BackupPruneRequest{Preview: preview}); err == nil {
				t.Fatal("pruned without a sound retained checkpoint")
			}
			if _, err = os.Stat(filepath.Join(one.Directory, "files.json")); err != nil {
				t.Fatal("failure deleted original checkpoint", err)
			}
		})
	}
}
