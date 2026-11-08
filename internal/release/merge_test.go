package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func armSet(t *testing.T) *Bundle {
	t.Helper()
	b, s := setFixture(t)
	if err := os.Remove(filepath.Join(b.Dir, "release-set.json")); err != nil {
		t.Fatal(err)
	}
	for i := range b.Manifest.Artifacts {
		b.Manifest.Artifacts[i].Arch = "arm64"
	}
	save(t, b.Dir, b.Manifest)
	b, err := Load(b.Dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Arch, s.ManifestSHA256 = "arm64", b.ManifestSHA256
	for i := range s.Artifacts {
		s.Artifacts[i].Arch = "arm64"
	}
	writeSet(t, b.Dir, s)
	b, err = Load(b.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFleetBundleRetainsBothPlatformsAndTools(t *testing.T) {
	a, _ := setFixture(t)
	b := armSet(t)
	target := filepath.Join(t.TempDir(), "fleet")
	merged, err := MergeTo(a.Dir, b.Dir, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Manifest.Artifacts) != 4 || len(merged.Artifacts()) != 8 || merged.Set.Arch != "multi" {
		t.Fatal("fleet lost a platform or operator tools")
	}
	for _, arch := range []string{"amd64", "arm64"} {
		for _, role := range []string{"server", "runner", "updater", "cli"} {
			if _, err := merged.SetArtifact(role, "linux", arch); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := MergeTo(a.Dir, b.Dir, target); err != nil {
		t.Fatal("same fleet merge is not replayable", err)
	}
	s := *b.Set
	s.Commit = strings.Repeat("b", 40)
	writeSet(t, b.Dir, s)
	if _, err := MergeTo(a.Dir, b.Dir, filepath.Join(t.TempDir(), "bad")); err == nil {
		t.Fatal("different sources were merged")
	}
}
