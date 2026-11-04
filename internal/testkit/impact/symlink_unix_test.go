//go:build !windows

package impact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSymlinkedResourceContentDriftRetainsActualFailure(t *testing.T) {
	for _, location := range []string{"ignored", "external", "tracked"} {
		t.Run(location, func(t *testing.T) {
			dir := fixtureRepo(t)
			data := filepath.Join(dir, ".local/data.txt")
			if location == "external" {
				data = filepath.Join(t.TempDir(), "data.txt")
			} else if location == "tracked" {
				data = filepath.Join(dir, "data.txt")
			}
			fixtureWrite(t, filepath.Dir(data), filepath.Base(data), "original")
			fixtureWrite(t, dir, "linked_test.go", "package fixture\nimport(\"os\";\"testing\")\nfunc TestLinkedResource(t *testing.T){b,err:=os.ReadFile(\"resource.txt\");if err!=nil||string(b)!=\"original\"{t.Fatal(\"linked resource changed\",err)}}\n")
			if err := os.Symlink(data, filepath.Join(dir, "resource.txt")); err != nil {
				t.Fatal(err)
			}
			fixtureCommit(t, dir)
			fixtureCommand(t, dir, "go", "test", "-race", "-count=1", "./...")
			before, err := Capture(t.Context(), dir, "go")
			if err != nil || before.UnboundResources {
				t.Fatal(before, err)
			}
			index := filepath.Join(dir, ".local/index.json")
			if err := WriteJSON(index, indexFor(before)); err != nil {
				t.Fatal(err)
			}
			fixtureWrite(t, filepath.Dir(data), filepath.Base(data), "changed")
			if err := SameSnapshot(t.Context(), dir, "go", before); err == nil {
				t.Fatal("symlink referent content change retained its old identity")
			}
			plan, err := PlanChecks(t.Context(), dir, "go", index, "", "")
			if err != nil {
				t.Fatal(err)
			}
			nativeSelectedFailure(t, dir, plan, "TestLinkedResource")
		})
	}
}

func TestUnboundResourceLinksRequireCompleteValidation(t *testing.T) {
	for _, kind := range []string{"directory", "dangling", "cycle"} {
		t.Run(kind, func(t *testing.T) {
			dir := fixtureRepo(t)
			target := filepath.Join(t.TempDir(), "missing")
			if kind == "directory" {
				target = t.TempDir()
			} else if kind == "cycle" {
				target = filepath.Join(dir, "resource")
			}
			if err := os.Symlink(target, filepath.Join(dir, "resource")); err != nil {
				t.Fatal(err)
			}
			fixtureCommit(t, dir)
			before, err := Capture(t.Context(), dir, "go")
			if err != nil || !before.UnboundResources {
				t.Fatal(before, err)
			}
			plan := Select(before, indexFor(before), "")
			if !plan.Full || !strings.Contains(strings.Join(plan.Reasons, "\n"), "unbound resource symlink") {
				t.Fatal(plan)
			}
			if _, err := Collect(t.Context(), CollectOptions{Dir: dir, Go: "go"}); err == nil {
				t.Fatal("unbound resource link published a function baseline")
			}
			fixtureCommand(t, dir, "go", "test", "-race", "-count=1", "./...")
		})
	}
}

func TestLinkedWebInspectionModulesUseEffectiveSource(t *testing.T) {
	dir, dependency := fixtureRepo(t), t.TempDir()
	fixtureWrite(t, dependency, "reader.test.ts", "import {readFileSync} from 'node:fs';\n")
	fixtureWrite(t, dir, "web/package.json", `{}`)
	if err := os.Symlink(filepath.Join(dependency, "reader.test.ts"), filepath.Join(dir, "web/reader.test.ts")); err != nil {
		t.Fatal(err)
	}
	before, err := Capture(t.Context(), dir, "go")
	if err != nil || !before.Files["web/reader.test.ts"].SourceInspection {
		t.Fatal("linked module lost its filesystem boundary", before, err)
	}
}

func TestCompilerOverlayOnLinkedFileUsesEffectiveCode(t *testing.T) {
	dir, backing := fixtureRepo(t), t.TempDir()
	fixtureWrite(t, backing, "original.go", "incomplete physical buffer")
	fixtureWrite(t, backing, "effective.go", "package fixture\nfunc A(n int)int{return 1}\nfunc B()int{return 2}\n")
	if err := os.Remove(filepath.Join(dir, "value.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(backing, "original.go"), filepath.Join(dir, "value.go")); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, ".local/overlay.json")
	if err := WriteJSON(manifest, map[string]any{"Replace": map[string]string{"value.go": filepath.Join(backing, "effective.go")}}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOFLAGS", strings.TrimSpace(os.Getenv("GOFLAGS")+" '-overlay="+manifest+"'"))
	source, err := Capture(t.Context(), dir, "go")
	if err != nil || source.Profile.Overlay == "" {
		t.Fatal(source, err)
	}
	fixtureCommand(t, dir, "go", "test", "-race", "-count=1", "./...")
	fixtureWrite(t, backing, "original.go", "changed physical runtime buffer")
	if err := SameSnapshot(t.Context(), dir, "go", source); err == nil {
		t.Fatal("overlay masked a linked physical input change")
	}
}
