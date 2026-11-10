package updater

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDataBackupIncludesOriginalArchivesAndRestoresMissingDirectories(t *testing.T) {
	root := t.TempDir()
	ledger := filepath.Join(root, "ledger.db")
	if err := os.WriteFile(ledger, []byte("original-ledger"), 0600); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(ledger+".files", "archives", "stream")
	if err := os.MkdirAll(filepath.Dir(original), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, []byte("original-prefix-and-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{DataFiles: []string{ledger}}
	operation := &Operation{BackupDir: filepath.Join(root, "backup")}
	if err := backupData(cfg, operation); err != nil {
		t.Fatal(err)
	}
	if len(operation.Backups) != 4 {
		t.Fatalf("backup did not include original: %+v", operation.Backups)
	}
	if err := os.WriteFile(ledger, []byte("replacement-ledger"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(ledger + ".files"); err != nil {
		t.Fatal(err)
	}
	if err := restoreData(cfg, operation); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{ledger: "original-ledger", original: "original-prefix-and-bytes"} {
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != want {
			t.Fatalf("restore %s: %q, %v", path, raw, err)
		}
	}
}

func TestOriginalBackupRejectsSymlinksAndCorruptionBeforeRestore(t *testing.T) {
	root := t.TempDir()
	ledger := filepath.Join(root, "ledger.db")
	if err := os.WriteFile(ledger, []byte("ledger"), 0600); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(ledger+".files", "archives", "stream")
	if err := os.MkdirAll(filepath.Dir(original), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, []byte("prefix"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{DataFiles: []string{ledger}}
	op := &Operation{BackupDir: filepath.Join(root, "snapshot")}
	if err := backupData(cfg, op); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(op.BackupDir, op.Backups[len(op.Backups)-1].Name), []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledger, []byte("unchanged-on-failure"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := restoreData(cfg, op); err == nil {
		t.Fatal("corrupt original backup restored")
	}
	raw, err := os.ReadFile(ledger)
	if err != nil || string(raw) != "unchanged-on-failure" {
		t.Fatal("restore changed ledger before validating all originals", err)
	}
	if err := os.Remove(original); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ledger, original); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := dataSnapshotPaths(cfg); err == nil {
		t.Fatal("original archive symlink accepted")
	}
}
