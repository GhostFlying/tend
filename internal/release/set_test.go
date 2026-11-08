package release

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setFixture(t *testing.T) (*Bundle, Set) {
	t.Helper()
	dir, m := fixture(t)
	raw, err := os.ReadFile(filepath.Join(dir, "runner"))
	if err != nil {
		t.Fatal(err)
	}
	artifacts := []Artifact{m.Artifacts[0]}
	for _, role := range []string{"server", "updater", "cli"} {
		a := m.Artifacts[0]
		a.Role, a.Path = role, role
		if err := os.WriteFile(filepath.Join(dir, role), raw, 0o755); err != nil {
			t.Fatal(err)
		}
		artifacts = append(artifacts, a)
		if role == "server" {
			m.Artifacts = append(m.Artifacts, a)
		}
	}
	save(t, dir, m)
	b, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := Set{Format: 1, ReleaseID: m.ReleaseID, Commit: strings.Repeat("a", 40), OS: "linux", Arch: "amd64", ManifestSHA256: b.ManifestSHA256, Artifacts: artifacts}
	writeSet(t, dir, s)
	b, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return b, s
}

func writeSet(t *testing.T, dir string, s Set) {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release-set.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteSetStagesOperatorTools(t *testing.T) {
	b, _ := setFixture(t)
	destination := filepath.Join(t.TempDir(), "staged")
	copy, err := b.CopyTo(destination)
	if err != nil {
		t.Fatal(err)
	}
	if copy.SetSHA256 != b.SetSHA256 || len(copy.Artifacts()) != 4 {
		t.Fatal("complete set was not preserved")
	}
	if _, err := copy.SetArtifact("updater", "linux", "amd64"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CopyTo(destination); err != nil {
		t.Fatal("same complete set is not replayable", err)
	}
	if err := os.Remove(filepath.Join(destination, "updater")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(destination); err == nil {
		t.Fatal("missing operator artifact was accepted")
	}
}

func TestCompleteSetCannotBeReplacedUnderServiceIdentity(t *testing.T) {
	b, set := setFixture(t)
	destination := filepath.Join(t.TempDir(), "staged")
	if _, err := b.CopyTo(destination); err != nil {
		t.Fatal(err)
	}
	set.Commit = strings.Repeat("b", 40)
	writeSet(t, b.Dir, set)
	other, err := Load(b.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.CopyTo(destination); err == nil {
		t.Fatal("changed set overwrote a service-identical release")
	}
}

func TestCompleteSetRejectsIncompleteOrConflictingMetadata(t *testing.T) {
	for _, kind := range []string{"missing", "duplicate", "path", "platform", "service", "manifest", "commit", "format", "corrupt", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			b, s := setFixture(t)
			switch kind {
			case "missing":
				s.Artifacts = s.Artifacts[:3]
			case "duplicate":
				s.Artifacts[3] = s.Artifacts[2]
			case "path":
				s.Artifacts[2].Path = "../updater"
			case "platform":
				s.Artifacts[2].Arch = "arm64"
			case "service":
				s.Artifacts[0].Path = "updater"
			case "manifest":
				s.ManifestSHA256 = strings.Repeat("0", 64)
			case "commit":
				s.Commit = strings.Repeat("A", 40)
			case "format":
				s.Format = 3
			case "corrupt":
				if err := os.WriteFile(filepath.Join(b.Dir, "updater"), []byte("changed"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(filepath.Join(b.Dir, "release-set.json")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(b.Dir, "missing"), filepath.Join(b.Dir, "release-set.json")); err != nil {
					t.Skip(err)
				}
				if _, err := Load(b.Dir); err == nil {
					t.Fatal("dangling release set symlink treated as old bundle")
				}
				return
			}
			writeSet(t, b.Dir, s)
			if _, err := Load(b.Dir); err == nil {
				t.Fatal("invalid complete set accepted")
			}
		})
	}
}

func TestSupervisorFirstReleaseFormatIsExplicitAndPreserved(t *testing.T) {
	b, s := setFixture(t)
	if b.Set.Format != 1 {
		t.Fatal("legacy distribution changed semantics")
	}
	s.Format = 2
	writeSet(t, b.Dir, s)
	modern, err := Load(b.Dir)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := modern.CopyTo(filepath.Join(t.TempDir(), "full"))
	if err != nil {
		t.Fatal(err)
	}
	if staged.Set.Format != 2 || staged.SetSHA256 != modern.SetSHA256 {
		t.Fatal("supervisor-first selection lost during staging")
	}
	arm := armSet(t)
	if _, err := MergeTo(modern.Dir, arm.Dir, filepath.Join(t.TempDir(), "mixed")); err == nil {
		t.Fatal("mixed execution semantics silently accepted")
	}
}
