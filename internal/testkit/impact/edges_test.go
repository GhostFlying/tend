package impact

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestExampleOutputAndUncoveredBodyCommentsRemainTestChanges(t *testing.T) {
	before := sourceFor(t, "core/a_test.go", "package core\nfunc Example(){println(1)\n// Output: 1\n}\n")
	pkg := before.Packages["example.test/m/core"]
	pkg.Roots = []string{"Example"}
	before.Packages[pkg.ImportPath] = pkg
	index := indexFor(before)
	after := before
	after.Files = map[string]File{}
	f, err := parseFile("core/a_test.go", pkg.ImportPath, []byte("package core\nfunc Example(){println(1)\n// Output: 2\n}\n"))
	if err != nil {
		t.Fatal(err)
	}
	after.Files["core/a_test.go"] = f
	p := Select(after, index, "")
	if !reflect.DeepEqual(p.Go[pkg.ImportPath], []string{"Example"}) {
		t.Fatal(p)
	}
}

func TestTransitiveProcessBoundariesUseStaticPackageFallback(t *testing.T) {
	dir := fixtureRepo(t)
	fixtureWrite(t, dir, "launcher/exec.go", "package launcher\nimport \"os/exec\"\nfunc Call(){_ = exec.Command(\"unused\")}\n")
	fixtureWrite(t, dir, "launcher_test.go", "package fixture\nimport \"example.test/fixture/launcher\"\nvar _ = launcher.Call\n")
	fixtureCommit(t, dir)
	s, err := Capture(t.Context(), dir, "go")
	if err != nil || !s.Packages[s.Module].ProcessBoundary {
		t.Fatal(s.Packages, err)
	}
	x := indexFor(s)
	x.Executions[0].Functions = []string{s.Module + ":A"}
	fixtureWrite(t, dir, "value.go", "package fixture\nfunc A(n int) int {if n>0{return 3};return 0}\nfunc B() int {return 2}\n")
	after, err := Capture(t.Context(), dir, "go")
	if err != nil {
		t.Fatal(err)
	}
	p := Select(after, x, "")
	if len(p.Go[s.Module]) != 4 {
		t.Fatal(p)
	}
}

func TestUnknownChildAndSourceReadersDoNotNeedAProductionImportEdge(t *testing.T) {
	before := sourceFor(t, "core/a.go", "package core\nfunc Value()int{return 1}\n")
	before.Packages["example.test/m/child-test"] = Package{ImportPath: "example.test/m/child-test", ProcessBoundary: true, Roots: []string{"TestSpawn"}}
	before.Packages["example.test/m/source-test"] = Package{ImportPath: "example.test/m/source-test", SourceInspection: true, Roots: []string{"TestSource"}}
	idx := indexFor(before)
	for n := range idx.Executions {
		if idx.Executions[n].Root == "TestA" {
			idx.Executions[n].Functions = []string{"example.test/m/core:Value"}
		}
	}
	after := before
	after.Files = map[string]File{}
	f, err := parseFile("core/a.go", "example.test/m/core", []byte("package core\nfunc Value()int{return 2}\n"))
	if err != nil {
		t.Fatal(err)
	}
	after.Files["core/a.go"] = f
	p := Select(after, idx, "")
	if !slices.Contains(p.Go["example.test/m/child-test"], "TestSpawn") || !slices.Contains(p.Go["example.test/m/source-test"], "TestSource") {
		t.Fatal(p)
	}
}

func TestTestOnlyProcessImportsDoNotPropagateThroughProduction(t *testing.T) {
	dir := fixtureRepo(t)
	fixtureWrite(t, dir, "launcher_test.go", "package fixture\nimport \"os/exec\"\nvar _ = exec.Command\n")
	fixtureWrite(t, dir, "consumer/value.go", "package consumer\nimport fixture \"example.test/fixture\"\nfunc Value()int{return fixture.B()}\n")
	fixtureWrite(t, dir, "consumer/value_test.go", "package consumer\nimport \"testing\"\nfunc TestValue(t *testing.T){if Value()!=2{t.Fatal(\"value\")}}\n")
	s, err := Capture(t.Context(), dir, "go")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Packages[s.Module].ProcessBoundary || s.Packages[s.Module+"/consumer"].ProcessBoundary {
		t.Fatal(s.Packages)
	}
}

func TestTimedOutRootCannotPublishCoverage(t *testing.T) {
	dir := fixtureRepo(t)
	fixtureWrite(t, dir, "slow_test.go", "package fixture\nimport (\"testing\";\"time\")\nfunc TestSlow(t *testing.T){time.Sleep(time.Second)}\n")
	x, err := Collect(t.Context(), CollectOptions{Dir: dir, Jobs: 2, Procs: 1, Timeout: time.Millisecond})
	if err == nil || x != nil {
		t.Fatal(x, err)
	}
}

func TestDiscoveryUsesTheRaceBuildConfiguration(t *testing.T) {
	dir := fixtureRepo(t)
	fixtureWrite(t, dir, "race_test.go", "//go:build race\n\npackage fixture\nimport \"testing\"\nfunc TestRaceOnly(t *testing.T) {}\n")
	x, err := Collect(t.Context(), CollectOptions{Dir: dir, Jobs: 2, Timeout: time.Minute})
	if err != nil || len(x.Executions) != 5 {
		t.Fatal(x, err)
	}
}

func TestIgnoredBuildAndEmbedInputsParticipateInSourceIdentity(t *testing.T) {
	dir := fixtureRepo(t)
	fixtureWrite(t, dir, ".gitignore", ".local/\nprivate.bin\nextra.go\n")
	fixtureWrite(t, dir, "value.go", "package fixture\nimport _ \"embed\"\n//go:embed private.bin\nvar Embedded string\nfunc A(n int)int{return 1}\nfunc B()int{return 2}\n")
	fixtureWrite(t, dir, "private.bin", "initial")
	fixtureWrite(t, dir, "extra.go", "package fixture\nfunc Extra()int{return 1}\n")
	fixtureCommit(t, dir)
	s, err := Capture(t.Context(), dir, "go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"private.bin", "extra.go"} {
		if _, ok := s.Files[path]; !ok {
			t.Fatalf("missing build input: %s", path)
		}
	}
	fixtureWrite(t, dir, "private.bin", "changed")
	if err := SameSource(t.Context(), dir, "go", s.Fingerprint); err == nil {
		t.Fatal("ignored embed change accepted")
	}
	current, err := Capture(t.Context(), dir, "go")
	if err != nil {
		t.Fatal(err)
	}
	if p := Select(current, indexFor(s), ""); !p.Full {
		t.Fatal(p)
	}
}

func TestRaceStaticCheckRejectsBrokenPackagesWithoutTestRoots(t *testing.T) {
	dir := fixtureRepo(t)
	fixtureWrite(t, dir, "Makefile", "go-check:\n\tgo vet ./...\n")
	s, err := Capture(t.Context(), dir, "go")
	if err != nil {
		t.Fatal(err)
	}
	fixtureWrite(t, dir, "untested/race.go", "//go:build race\n\npackage untested\nfunc Broken(){missing()}\n")
	after, err := Capture(t.Context(), dir, "go")
	if err != nil {
		t.Fatal(err)
	}
	p := Select(after, indexFor(s), "")
	if len(p.Go) != 0 {
		t.Fatal("unexpected runtime tests for an independent untested package", p)
	}
	if _, err := output(t.Context(), dir, "go", "vet", "-race", "./..."); err == nil {
		t.Fatal("race-only compile failure escaped static validation")
	}
	plan := filepath.Join(dir, ".local/plan.json")
	if err := WriteJSON(plan, p); err != nil {
		t.Fatal(err)
	}
	r, err := Run(t.Context(), RunOptions{Dir: dir, Go: "go", PlanPath: plan, Jobs: 1, Procs: 1, Packages: 1}, p)
	if err == nil || r.Passed || !r.Valid {
		t.Fatal("historical go-check bypassed the race compile guard", r, err)
	}
}

func TestCumulativeHistoryAndDirtyNewRoots(t *testing.T) {
	dir := fixtureRepo(t)
	s, err := Capture(t.Context(), dir, "go")
	if err != nil {
		t.Fatal(err)
	}
	idx := indexFor(s)
	for n := range idx.Executions {
		e := &idx.Executions[n]
		if e.Root == "TestA" {
			e.Functions = []string{s.Module + ":A"}
		}
		if e.Root == "TestB" {
			e.Functions = []string{s.Module + ":B"}
		}
	}
	indexPath := filepath.Join(dir, ".local/index.json")
	if err := WriteJSON(indexPath, idx); err != nil {
		t.Fatal(err)
	}
	fixtureWrite(t, dir, "value.go", "package fixture\nfunc A(n int) int {if n>0{return 3};return 0}\nfunc B() int {return 2}\n")
	intermediate := fixtureCommit(t, dir)
	fixtureWrite(t, dir, "value.go", "package fixture\nfunc A(n int) int {if n>0{return 3};return 0}\nfunc B() int {return 4}\n")
	fixtureCommit(t, dir)
	p, err := PlanChecks(t.Context(), dir, "go", indexPath, intermediate, "")
	if err != nil || p.Full || !slices.Contains(p.Go[s.Module], "TestA") || !slices.Contains(p.Go[s.Module], "TestB") {
		t.Fatal(p, err)
	}
	fixtureWrite(t, dir, "new_test.go", "package fixture\nimport \"testing\"\nfunc TestNew(t *testing.T){}\n")
	p, err = PlanChecks(t.Context(), dir, "go", indexPath, "", "")
	if err != nil || !slices.Contains(p.Go[s.Module], "TestNew") {
		t.Fatal(p, err)
	}
}

func TestWebAndBrowserFallbacks(t *testing.T) {
	for _, sample := range []struct {
		path          string
		full, browser bool
	}{
		{"web/src/value.ts", false, true},
		{"web/src/value.css", true, true},
		{"web/src/value.mock.ts", true, true},
		{"web/src/value.snap", true, true},
		{"web/vite.config.ts", true, true},
		{"web/index.html", true, true},
		{"web/scripts/one.ts", true, false},
	} {
		t.Run(sample.path, func(t *testing.T) {
			dir := fixtureRepo(t)
			fixtureWrite(t, dir, "web/package.json", `{"scripts":{"test:one":"node scripts/one.ts","test:two":"node scripts/two.ts"}}`)
			fixtureWrite(t, dir, "web/scripts/layout-groups.ts", `const groups=["test:one","test:two"]`)
			fixtureWrite(t, dir, sample.path, "before")
			fixtureCommit(t, dir)
			s, err := Capture(t.Context(), dir, "go")
			if err != nil {
				t.Fatal(err)
			}
			indexPath := filepath.Join(dir, ".local/index.json")
			if err := WriteJSON(indexPath, indexFor(s)); err != nil {
				t.Fatal(err)
			}
			fixtureWrite(t, dir, sample.path, "after")
			p, err := PlanChecks(t.Context(), dir, "go", indexPath, "", "")
			if err != nil || !p.Web.Run || p.Web.Full != sample.full {
				t.Fatal(p, err)
			}
			want := []string{"test:one"}
			if sample.browser {
				want = append(want, "test:two")
			}
			if !reflect.DeepEqual(p.Layout, want) {
				t.Fatal(p.Layout, want)
			}
		})
	}
}

func TestMalformedIndexesFallBackWithoutTrustingPartialCoverage(t *testing.T) {
	dir := fixtureRepo(t)
	s, err := Capture(t.Context(), dir, "go")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".local/index.json")
	for _, mode := range []string{"dirty", "digest", "coverage", "duplicate", "toolchain"} {
		t.Run(mode, func(t *testing.T) {
			x := indexFor(s)
			switch mode {
			case "dirty":
				x.Source.Clean = false
			case "digest":
				x.Source.Fingerprint = "bad"
			case "coverage":
				x.Executions[0].Functions = []string{"unknown"}
			case "duplicate":
				x.Executions = append(x.Executions, x.Executions[0])
			case "toolchain":
				x.Source.Profile.Go = "different"
			}
			if err := WriteJSON(path, x); err != nil {
				t.Fatal(err)
			}
			p, err := PlanChecks(t.Context(), dir, "go", path, "", "")
			if err != nil || !p.Full || len(p.Go[s.Module]) != 4 {
				t.Fatal(p, err)
			}
		})
	}
	fixtureWrite(t, dir, ".local/index.json", strings.Repeat("{", 20))
	p, err := PlanChecks(t.Context(), dir, "go", path, "", "")
	if err != nil || !p.Full {
		t.Fatal(p, err)
	}
}

func TestGoResourceReaderRetainsActualFailureAfterWebOnlyChange(t *testing.T) {
	dir := fixtureRepo(t)
	fixtureWrite(t, dir, "web/src/value.ts", "export const value = 1;\n")
	fixtureWrite(t, dir, "web/package.json", `{"scripts":{"test:one":"node scripts/one.ts"}}`)
	fixtureWrite(t, dir, "web/scripts/layout-groups.ts", `const groups=["test:one"]`)
	fixtureWrite(t, dir, "web/scripts/one.ts", "// Browser inventory fixture.\n")
	fixtureWrite(t, dir, "resource_test.go", `package fixture
import (
 "os"
 "strings"
 "testing"
)
func TestWebResource(t *testing.T) {
 b,err:=os.ReadFile("web/src/value.ts")
 if err!=nil {t.Fatal(err)}
 if !strings.Contains(string(b), "value = 1") {t.Fatal("Web resource contract changed")}
}
`)
	fixtureCommit(t, dir)
	before, err := Capture(t.Context(), dir, "go")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".local/index.json")
	if err := WriteJSON(path, indexFor(before)); err != nil {
		t.Fatal(err)
	}
	fixtureWrite(t, dir, "web/src/value.ts", "export const value = 2;\n")
	var full strings.Builder
	fullErr := Command(t.Context(), dir, &full, nil, "go", "test", "-race", "-count=1", "-json", "./...")
	if fullErr == nil || !strings.Contains(full.String(), `"Action":"fail","Package":"example.test/fixture","Test":"TestWebResource"`) {
		t.Fatalf("resource mutant did not fail its real Go consumer: %s / %v", full.String(), fullErr)
	}
	plan, err := PlanChecks(t.Context(), dir, "go", path, "", "")
	if err != nil || !slices.Contains(plan.Go[before.Module], "TestWebResource") {
		t.Fatalf("actual Go consumer omitted after Web-only change: %+v / %v", plan, err)
	}
	var selected strings.Builder
	pattern := "^(" + strings.Join(plan.Go[before.Module], "|") + ")$"
	selectedErr := Command(t.Context(), dir, &selected, nil, "go", "test", "-race", "-count=1", "-json", "-run", pattern, "./...")
	if selectedErr == nil || !strings.Contains(selected.String(), `"Action":"fail","Package":"example.test/fixture","Test":"TestWebResource"`) {
		t.Fatalf("selected roots did not execute the failing resource consumer: %s / %v", selected.String(), selectedErr)
	}
	t.Log("resource counterexample full native events:\n" + full.String())
	t.Log("resource counterexample selected native events:\n" + selected.String())
}

func TestGoResourceBoundaryDetectionHandlesModuleAliases(t *testing.T) {
	for _, module := range []string{"os", "io/fs", "io/ioutil", "path/filepath", "syscall", "golang.org/x/sys/unix", "go/parser", "go/build", "C"} {
		t.Run(module, func(t *testing.T) {
			f, err := parseFile("read_test.go", "m", []byte("package p\nimport renamed \""+module+"\"\n"))
			if err != nil || !f.SourceInspection {
				t.Fatal(f, err)
			}
		})
	}
}

func TestUnknownGoConsumersRemainAfterWebAndDocumentationChanges(t *testing.T) {
	for _, path := range []string{"web/src/value.ts", "web/src/value.css", "docs/readme.md"} {
		t.Run(path, func(t *testing.T) {
			before := sourceFor(t, "core/a.go", "package core\nfunc Value()int{return 1}\n")
			before.Packages["example.test/m/child"] = Package{ImportPath: "example.test/m/child", ProcessBoundary: true, Roots: []string{"TestChild"}}
			before.Packages["example.test/m/read"] = Package{ImportPath: "example.test/m/read", SourceInspection: true, Roots: []string{"TestRead"}}
			f, err := parseFile(path, "", []byte("first"))
			if err != nil {
				t.Fatal(err)
			}
			before.Files[path] = f
			idx := indexFor(before)
			after := before
			after.Files = map[string]File{}
			for name, file := range before.Files {
				after.Files[name] = file
			}
			f, err = parseFile(path, "", []byte("second"))
			if err != nil {
				t.Fatal(err)
			}
			after.Files[path] = f
			p := Select(after, idx, "")
			if !slices.Contains(p.Go["example.test/m/child"], "TestChild") || !slices.Contains(p.Go["example.test/m/read"], "TestRead") {
				t.Fatal(p)
			}
		})
	}
}
