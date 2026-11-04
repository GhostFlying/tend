package impact

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func dependencyFixture(t *testing.T, value string) string {
	t.Helper()
	dir := t.TempDir()
	fixtureWrite(t, dir, "go.mod", "module example.test/dependency\n\ngo 1.27.1\n")
	fixtureWrite(t, dir, "value.go", "package dependency\nfunc Value()int{return "+value+"}\n")
	return dir
}

func dependencyConsumer(t *testing.T, dir string) {
	t.Helper()
	fixtureWrite(t, dir, "dependency_test.go", "package fixture\nimport(\"testing\";\"example.test/dependency\")\nfunc TestDependency(t *testing.T){if dependency.Value()!=1{t.Fatal(\"changed dependency\")}}\n")
}

func assertModuleMutation(t *testing.T, dir string, before Snapshot) {
	t.Helper()
	path := filepath.Join(dir, ".local/index.json")
	if err := WriteJSON(path, indexFor(before)); err != nil {
		t.Fatal(err)
	}
	if err := SameSnapshot(t.Context(), dir, "go", before); err == nil {
		t.Fatal("mutable effective module input retained its old identity")
	}
	plan, err := PlanChecks(t.Context(), dir, "go", path, "", "")
	if err != nil || !plan.Full {
		t.Fatal(plan, err)
	}
	nativeSelectedFailure(t, dir, plan, "TestDependency")
}

func TestAlternateModuleContentDriftRequiresCompleteValidation(t *testing.T) {
	for _, location := range []string{"ignored", "external", "quoted_relative"} {
		t.Run(location, func(t *testing.T) {
			dir, first, second := fixtureRepo(t), dependencyFixture(t, "1"), dependencyFixture(t, "2")
			dependencyConsumer(t, dir)
			fixtureCommit(t, dir)
			manifest := filepath.Join(dir, ".local/alternate.mod")
			if location == "external" {
				manifest = filepath.Join(t.TempDir(), "alternate.mod")
			} else if location == "quoted_relative" {
				manifest = ".local/config with spaces/alternate.mod"
			}
			body := func(dep string) string {
				return "module example.test/fixture\n\ngo 1.27.1\nrequire example.test/dependency v0.0.0\nreplace example.test/dependency => " + strconv.Quote(filepath.ToSlash(dep)) + "\n"
			}
			write := func(dep string) {
				fixtureWrite(t, filepath.Dir(buildPath(dir, manifest)), filepath.Base(manifest), body(dep))
			}
			write(first)
			t.Setenv("GOFLAGS", strings.TrimSpace(os.Getenv("GOFLAGS")+" '-modfile="+manifest+"'"))
			before, err := Capture(t.Context(), dir, "go")
			if err != nil || before.Profile.ModuleConfig == "" || before.Profile.LocalModules == "" {
				t.Fatal(before, err)
			}
			fixtureCommand(t, dir, "go", "test", "-race", "-count=1", "./...")
			if _, err := Collect(t.Context(), CollectOptions{Dir: dir, Go: "go"}); err == nil {
				t.Fatal("alternate module published a function coverage baseline")
			}
			write(second)
			assertModuleMutation(t, dir, before)
		})
	}
}

func TestLocalReplacementBuildInputsRemainBound(t *testing.T) {
	dir, dep := fixtureRepo(t), dependencyFixture(t, "1")
	dependencyConsumer(t, dir)
	fixtureWrite(t, dir, "go.mod", "module example.test/fixture\n\ngo 1.27.1\nrequire example.test/dependency v0.0.0\nreplace example.test/dependency => "+strconv.Quote(filepath.ToSlash(dep))+"\n")
	fixtureCommit(t, dir)
	fixtureCommand(t, dir, "go", "test", "-race", "-count=1", "./...")
	before, err := Capture(t.Context(), dir, "go")
	if err != nil || before.Profile.LocalModules == "" || !before.Packages[before.Module].SourceInspection {
		t.Fatal(before, err)
	}
	fixtureWrite(t, dep, "value.go", "package dependency\nfunc Value()int{return 2}\n")
	assertModuleMutation(t, dir, before)
}

func TestWorkspaceConfigurationAndMutableModulesRemainBound(t *testing.T) {
	dir, first, second := fixtureRepo(t), dependencyFixture(t, "1"), dependencyFixture(t, "2")
	dependencyConsumer(t, dir)
	fixtureWrite(t, dir, "go.mod", "module example.test/fixture\n\ngo 1.27.1\nrequire example.test/dependency v0.0.0\n")
	fixtureCommit(t, dir)
	work := filepath.Join(t.TempDir(), "go.work")
	write := func(dep string) {
		fixtureWrite(t, filepath.Dir(work), filepath.Base(work), "go 1.27.1\nuse (\n"+strconv.Quote(filepath.ToSlash(dir))+"\n"+strconv.Quote(filepath.ToSlash(dep))+"\n)\n")
	}
	write(first)
	t.Setenv("GOWORK", work)
	fixtureCommand(t, dir, "go", "test", "-race", "-count=1", "./...")
	before, err := Capture(t.Context(), dir, "go")
	if err != nil || before.Module != "example.test/fixture" || before.Profile.ModuleConfig == "" || before.Profile.LocalModules == "" {
		t.Fatal(before, err)
	}
	write(second)
	assertModuleMutation(t, dir, before)
}
