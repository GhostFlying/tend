package release

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T) (string, Manifest) {
	t.Helper()
	dir := t.TempDir()
	raw := []byte("an executable artifact")
	sum := sha256.Sum256(raw)
	m := Manifest{Format: 1, ReleaseID: "v1", Protocol: Range{Min: 1, Max: 2}, Storage: Storage{ServerSchema: Range{Min: 26, Max: 27}, RunnerJournal: Range{Min: 8, Max: 9}}, Artifacts: []Artifact{{Role: "runner", OS: "linux", Arch: "amd64", Path: "runner", Size: int64(len(raw)), SHA256: hex.EncodeToString(sum[:])}}}
	if err := os.WriteFile(filepath.Join(dir, "runner"), raw, 0o755); err != nil {
		t.Fatal(err)
	}
	save(t, dir, m)
	return dir, m
}
func save(t *testing.T, dir string, m Manifest) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}
func TestBundleIntegrityAndIdentity(t *testing.T) {
	dir, m := fixture(t)
	b, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidSHA(b.ManifestSHA256) {
		t.Fatal("missing fixed manifest hash")
	}
	if _, err := b.Artifact("server", "linux", "amd64"); err == nil {
		t.Fatal("missing platform accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "runner"), []byte("a modified executable!"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("corrupt artifact accepted")
	}
	m.Artifacts[0].Path = "../outside"
	save(t, dir, m)
	if _, err := Load(dir); err == nil {
		t.Fatal("path escape accepted")
	}
}
func TestBundleRejectsSymlinkAndDuplicateTargets(t *testing.T) {
	dir, m := fixture(t)
	p := filepath.Join(dir, "runner")
	outside := filepath.Join(t.TempDir(), "outside")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, p); err != nil {
		t.Skip(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("symlink accepted")
	}
	m.Artifacts = append(m.Artifacts, m.Artifacts[0])
	if err := m.Validate(); err == nil {
		t.Fatal("duplicate platform accepted")
	}
}
func TestBundleRejectsUnknownMetadata(t *testing.T) {
	dir, _ := fixture(t)
	p := filepath.Join(dir, "manifest.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw[:len(raw)-1], []byte(`,"unexpected":true}`)...)
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("unknown release metadata accepted")
	}
}

func TestCopyBundleRetainsIndependentVerifiedIdentity(t *testing.T) {
	dir, _ := fixture(t)
	b, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "staged")
	staged, err := b.CopyTo(destination)
	if err != nil {
		t.Fatal(err)
	}
	if staged.ManifestSHA256 != b.ManifestSHA256 {
		t.Fatal("staged identity changed")
	}
	if _, err := b.CopyTo(destination); err != nil {
		t.Fatal("identical staging replay failed", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(destination); err != nil {
		t.Fatal("staging still depends on source", err)
	}
}
